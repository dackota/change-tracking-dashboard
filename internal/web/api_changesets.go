// Package web (this file): the GET /api/changesets JSON endpoint. It hands
// the raw query params to changesetquery, which owns every decision between a
// request and a page — parsing, clamping, predicate composition, the
// risk-rule snapshot, the cursor contract — and marshals the result. This
// file is transport: decode, delegate, encode.
package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/changesetquery"
	"github.com/dackota/change-tracking-dashboard/internal/store"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
)

// genericServerErrorMsg is the only text sent to the client on an internal
// failure. Detail is logged server-side only.
const genericServerErrorMsg = "internal server error"

// ChangesetsHandler serves GET /api/changesets as JSON.
type ChangesetsHandler struct {
	st    *store.Store
	risk  RiskRulesSource
	query *changesetquery.Querier
}

// ChangesetsOption configures a ChangesetsHandler at construction.
type ChangesetsOption func(*ChangesetsHandler)

// WithChangesetsRiskRules sets the source of risk rules the handler classifies
// each changeset against. When unset, the handler falls back to the built-in
// changeset.DefaultRiskRules (see riskRulesOrDefault).
func WithChangesetsRiskRules(src RiskRulesSource) ChangesetsOption {
	return func(h *ChangesetsHandler) { h.risk = src }
}

// NewChangesetsHandler creates a ChangesetsHandler backed by the given store.
func NewChangesetsHandler(st *store.Store, opts ...ChangesetsOption) *ChangesetsHandler {
	h := &ChangesetsHandler{st: st}
	for _, opt := range opts {
		opt(h)
	}
	// The rules supplier is a closure, not a captured snapshot: h.risk is a
	// live, hot-reloading source, and each query must classify against the
	// rules current at that moment.
	h.query = changesetquery.New(st, func() []changeset.RiskRule { return riskRulesOrDefault(h.risk) })
	return h
}

// changesetsResponse is the top-level JSON response body.
type changesetsResponse struct {
	Changesets []changesetJSON `json:"changesets"`
	NextCursor string          `json:"nextCursor"`
}

// changesetJSON is the explicit JSON shape for one Changeset. Defined here
// (rather than relying on changeset.Changeset's Go struct tags) so the wire
// format is decoupled from internal field naming.
//
// Risk is a Changeset-level, query-time projection (changeset.ClassifyRisk
// against changeset.DefaultRiskRules) — like Kind, it is never stored, only
// computed on read — so the feed the browser renders from this endpoint
// carries each changeset's risk badge (R24) without a schema migration.
// Marshaled as "risk": [] (never omitted, never null) so a changeset with no
// risk classes still renders as an explicit empty list, matching
// ClassifyRisk's own "empty set is a valid result" contract.
//
// Impact is a Changeset-level, query-time projection (changeset.ClassifyImpact)
// — computed on read like Risk and Kind, never stored. Unlike Risk it is
// always exactly one of the tier strings, never omitted/empty/null, so a
// changeset with no comparable version change still carries "other" rather
// than a blank field.
type changesetJSON struct {
	Repo        string   `json:"repo"`
	CommitSha   string   `json:"commitSha"`
	Author      string   `json:"author"`
	CommittedAt string   `json:"committedAt"`
	IssueRefs   []string `json:"issueRefs,omitempty"`
	// Subject is the commit message's first line (#85), omitted when empty
	// (pre-#85 rows) so the client falls back to the SHA.
	Subject string       `json:"subject,omitempty"`
	Changes []changeJSON `json:"changes"`
	Risk    []string     `json:"risk"`
	Impact  string       `json:"impact"`
}

// changeJSON is the explicit JSON shape for one Change within a Changeset.
type changeJSON struct {
	Field      string  `json:"field"`
	Key        *string `json:"key,omitempty"`
	ChangeType string  `json:"changeType"`
	OldValue   *string `json:"oldValue,omitempty"`
	NewValue   *string `json:"newValue,omitempty"`
	Kind       string  `json:"kind"`
}

// ServeHTTP satisfies http.Handler.
func (h *ChangesetsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "application/json")

	page, err := h.query.Run(r.Context(), r.URL.Query())
	if err != nil {
		// Detail is logged server-side; the client gets a generic message so
		// internal detail (SQLite errors, cursor bytes) and echoed caller
		// input never reach it.
		telemetry.LoggerFromContext(r.Context()).Error("web: query changesets", "error", err)
		if errors.Is(err, changesetquery.ErrBadRequest) {
			http.Error(w, genericBadRequestMsg, http.StatusBadRequest)
			return
		}
		http.Error(w, genericServerErrorMsg, http.StatusInternalServerError)
		return
	}

	writeJSON(r, w, http.StatusOK, changesetsResponse{
		// page.Rules is the exact snapshot the filter classified against —
		// deliberately not a second read of the rules source, which is a live
		// hot-reloading watcher. Re-reading here would let a reload land
		// between filtering and rendering and produce a response whose risk[]
		// badges contradict the filter that selected the changesets.
		Changesets: toChangesetsJSON(page.Changesets, page.Rules),
		NextCursor: page.NextCursor,
	})
}

// genericBadRequestMsg is the only text sent to the client for a malformed
// request (bad asOf, bad cursor, invalid facet key). Caller input is never
// echoed back.
const genericBadRequestMsg = "bad request"

// toChangesetsJSON converts a slice of changeset.Changeset to their explicit
// JSON shape, classifying each against rules.
func toChangesetsJSON(sets []changeset.Changeset, rules []changeset.RiskRule) []changesetJSON {
	out := make([]changesetJSON, 0, len(sets))
	for _, cs := range sets {
		out = append(out, toChangesetJSON(cs, rules))
	}
	return out
}

// toChangesetJSON converts a single changeset.Changeset to its explicit JSON
// shape, classifying its risk against rules.
func toChangesetJSON(cs changeset.Changeset, rules []changeset.RiskRule) changesetJSON {
	changes := make([]changeJSON, 0, len(cs.Changes))
	for _, c := range cs.Changes {
		changes = append(changes, changeJSON{
			Field:      c.Field,
			Key:        c.Key,
			ChangeType: string(c.ChangeType),
			OldValue:   c.OldValue,
			NewValue:   c.NewValue,
			Kind:       string(c.Kind),
		})
	}
	return changesetJSON{
		Repo:        cs.Repo,
		CommitSha:   cs.CommitSha,
		Author:      cs.Author,
		CommittedAt: cs.CommittedAt.UTC().Format(time.RFC3339Nano),
		IssueRefs:   cs.IssueRefs,
		Subject:     cs.Subject,
		Changes:     changes,
		Risk:        toRiskStrings(changeset.ClassifyRisk(cs, rules)),
		Impact:      string(changeset.ClassifyImpact(cs)),
	}
}

// toRiskStrings converts a []changeset.Risk into its wire []string form,
// always non-nil so an empty risk set marshals as "risk": [] rather than
// "risk": null.
func toRiskStrings(risks []changeset.Risk) []string {
	out := make([]string, 0, len(risks))
	for _, r := range risks {
		out = append(out, string(r))
	}
	return out
}

// writeJSON marshals v and writes it with the given status code, setting the
// Content-Type itself.
//
// Owning the header here rather than relying on the caller to have set it is
// what makes this safe for handlers that choose their representation at render
// time: a negotiated handler cannot know at the top of ServeHTTP whether it
// will emit JSON, and a Content-Type set before the body is chosen is a
// Content-Type that will eventually describe the wrong body.
func writeJSON(r *http.Request, w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		telemetry.LoggerFromContext(r.Context()).Error("web: marshal changesets response", "error", err)
		http.Error(w, genericServerErrorMsg, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		// The status code is already on the wire, so there is nothing to do but
		// record it — previously discarded outright, which hid genuine write
		// failures alongside the harmless client-disconnect ones.
		logResponseWriteError(r.Context(), "web: write changesets response", err)
	}
}
