// Package web (this file): the GET /api/changesets JSON endpoint. It parses
// the request (asOf, tri-state facet params, repo scope, cursor, limit),
// delegates all querying/grouping/filtering to store.QueryChangesets, and
// marshals the result. No query/grouping/classification/filter logic lives
// here — that stays server-side in store/changeset/filter, as it already
// does for the HTML feed handler.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/filter"
	"github.com/dackota/change-tracking-dashboard/internal/store"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// genericServerErrorMsg is the only text sent to the client on an internal
// failure. Detail is logged server-side only.
const genericServerErrorMsg = "internal server error"

// maxChangesetPageSize is the hard server-side cap on the number of
// Changesets returned per page, regardless of what the caller requests via
// the limit param. This closes the deferred MEDIUM from the store-changeset-
// query slice (unbounded row fetch): the endpoint never passes a caller-
// dictated limit straight through to the store — it is always clamped here
// first.
const maxChangesetPageSize = 100

// defaultChangesetPageSize is used when the caller omits the limit param.
// Kept comfortably under maxChangesetPageSize.
const defaultChangesetPageSize = 50

// reservedChangesetsParams are query-param names that are never treated as
// facet filters, regardless of whether a stored Change happens to carry a
// facet with the same name.
var reservedChangesetsParams = map[string]struct{}{
	"asOf":   {},
	"cursor": {},
	"impact": {},
	"limit":  {},
	"repo":   {},
	"risk":   {},
	"since":  {},
}

// ChangesetsHandler serves GET /api/changesets as JSON.
type ChangesetsHandler struct {
	st   *store.Store
	risk RiskRulesSource
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

	logger := telemetry.LoggerFromContext(r.Context())

	asOf, err := parseAsOf(r.URL.Query().Get("asOf"))
	if err != nil {
		logger.Error("web: parse asOf", "error", err)
		http.Error(w, genericBadRequestMsg, http.StatusBadRequest)
		return
	}

	since, err := parseSince(r.URL.Query().Get("since"))
	if err != nil {
		logger.Error("web: parse since", "error", err)
		http.Error(w, genericBadRequestMsg, http.StatusBadRequest)
		return
	}
	window := store.TimeWindow{Since: since, AsOf: asOf}

	// Fetch the set of known facet names first. URL query-param keys are
	// whitelisted against this set before reaching filter.Parse, mirroring
	// the HTML feed handler's boundary guard.
	facetOpts, err := h.st.FacetOptions()
	if err != nil {
		logger.Error("web: facet options", "error", err)
		http.Error(w, genericServerErrorMsg, http.StatusInternalServerError)
		return
	}

	spec, err := parseChangesetsFilter(r.URL.Query(), facetOpts)
	if err != nil {
		logger.Error("web: parse facet filter", "error", err)
		http.Error(w, genericBadRequestMsg, http.StatusBadRequest)
		return
	}
	// The repo scope (R26) is a single distinguished value, not a tri-state
	// facet — it is read directly from the reserved "repo" param and applied
	// to spec via WithRepo, composing with the facet filter via AND (R27).
	// An absent/empty repo param is a no-op: WithRepo("") matches any repo.
	spec = spec.WithRepo(r.URL.Query().Get("repo"))

	// The impact filter is a flat allow-set over a closed vocabulary, not a
	// facet, so it is parsed separately and never reaches filter.Parse. An
	// unrecognized tier is a 400 rather than being dropped — see
	// filter.ParseClassSet for why this diverges from facet-param handling.
	impacts, err := filter.ParseClassSet(r.URL.Query()["impact"], changeset.ImpactTiers())
	if err != nil {
		logger.Error("web: parse impact filter", "error", err)
		http.Error(w, genericBadRequestMsg, http.StatusBadRequest)
		return
	}

	// The risk filter parses the same way, against the slug vocabulary rather
	// than the display values — see changeset.riskSlugs for why the wire never
	// uses the display form.
	risks, err := filter.ParseClassSet(r.URL.Query()["risk"], changeset.RiskSlugs())
	if err != nil {
		logger.Error("web: parse risk filter", "error", err)
		http.Error(w, genericBadRequestMsg, http.StatusBadRequest)
		return
	}

	cursor := r.URL.Query().Get("cursor")

	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		logger.Error("web: parse limit", "error", err)
		http.Error(w, genericBadRequestMsg, http.StatusBadRequest)
		return
	}

	// Both the filter predicate and the response's risk[] field classify
	// against this one value, so they can never disagree about an operator's
	// configured rules.
	rules := riskRulesOrDefault(h.risk)
	pred := allPredicates(impactPredicate(impacts), riskPredicate(risks, rules))

	var page store.ChangesetPage
	err = telemetry.WithSpan(r.Context(), tracer, "store.query_changesets", func(ctx context.Context) error {
		var err error
		page, err = h.st.QueryChangesets(window, spec, pred, cursor, limit)
		if err != nil {
			return err
		}
		// Record the page-fill loop's work: how many commits were examined
		// against how many survived. The ratio is what makes a pathological
		// class filter diagnosable from a trace — a span reporting 5000
		// examined for 3 returned is a filter worth investigating, and
		// without the examined count that span is indistinguishable from a
		// cheap query that returned 3.
		trace.SpanFromContext(ctx).SetAttributes(
			attribute.Int("changesets.examined", page.Examined),
			attribute.Int("changesets.returned", len(page.Changesets)),
		)
		return nil
	})
	if err != nil {
		// Log the detail server-side; return a generic message so internal
		// details (SQLite errors, cursor bytes) don't leak to the client. An
		// invalid cursor is caller input (400); anything else is treated as
		// a store failure (500).
		logger.Error("web: query changesets", "error", err)
		if errors.Is(err, store.ErrInvalidCursor) {
			http.Error(w, genericBadRequestMsg, http.StatusBadRequest)
			return
		}
		http.Error(w, genericServerErrorMsg, http.StatusInternalServerError)
		return
	}

	resp := changesetsResponse{
		// The same rules value the predicate closed over — deliberately not a
		// second riskRulesOrDefault call. The source is a live, hot-reloading
		// watcher, so re-reading it here would let a reload land between
		// filtering and rendering and produce a response whose risk[] badges
		// contradict the filter that selected the changesets.
		Changesets: toChangesetsJSON(page.Changesets, rules),
		NextCursor: page.NextCursor,
	}
	writeJSON(r, w, http.StatusOK, resp)
}

// impactPredicate adapts an impact allow-set into the opaque predicate the
// store applies after assembly. An empty set yields a nil predicate rather
// than one that always returns true: nil is the store's own "no filtering"
// signal, so an absent impact param costs nothing — no per-changeset
// classification, no page-fill loop iterations beyond the first fetch.
func impactPredicate(impacts filter.ClassSet) store.ChangesetPredicate {
	if impacts.Empty() {
		return nil
	}
	return func(cs changeset.Changeset) bool {
		return impacts.Allows(string(changeset.ClassifyImpact(cs)))
	}
}

// riskPredicate adapts a risk-slug allow-set into a store predicate, closing
// over the rule set to classify against. Taking rules as a parameter rather
// than reading a package default is what makes the filter and the risk badges
// agree by construction: the handler passes the same riskRulesOrDefault(h.risk)
// value to both, so an operator's configured rules cannot apply to one and not
// the other.
//
// A changeset matches when its risk set intersects the requested set, so a
// changeset with no risk classes never matches a non-empty filter — there is
// nothing to intersect with.
func riskPredicate(risks filter.ClassSet, rules []changeset.RiskRule) store.ChangesetPredicate {
	if risks.Empty() {
		return nil
	}
	return func(cs changeset.Changeset) bool {
		for _, r := range changeset.ClassifyRisk(cs, rules) {
			slug, ok := changeset.SlugForRisk(r)
			if ok && risks.Allows(slug) {
				return true
			}
		}
		return false
	}
}

// allPredicates combines predicates with AND, skipping nil ones. It returns
// nil when nothing is left, preserving the store's "no predicate" fast path
// for the common unfiltered request.
//
// AND is the only correct composition here: impact and risk answer different
// questions about the same changeset, so a request naming both is asking for
// changesets satisfying both. Returning the last non-nil predicate instead
// would pass every single-filter test and silently drop a filter whenever two
// are combined.
func allPredicates(preds ...store.ChangesetPredicate) store.ChangesetPredicate {
	active := make([]store.ChangesetPredicate, 0, len(preds))
	for _, p := range preds {
		if p != nil {
			active = append(active, p)
		}
	}
	switch len(active) {
	case 0:
		return nil
	case 1:
		return active[0]
	}
	return func(cs changeset.Changeset) bool {
		for _, p := range active {
			if !p(cs) {
				return false
			}
		}
		return true
	}
}

// genericBadRequestMsg is the only text sent to the client for a malformed
// request (bad asOf, bad cursor, invalid facet key). Caller input is never
// echoed back.
const genericBadRequestMsg = "bad request"

// parseChangesetsFilter builds a filter.FilterSpec from the request's query
// params, restricted to known facet names (from knownFacets) minus the
// reserved params (asOf, cursor, limit, repo). Reserved params are never
// treated as facets even if a stored Change happens to carry a same-named
// facet. An unknown, non-reserved param name (typo, unrelated param) is
// silently ignored rather than rejected — matching the HTML feed handler's
// existing whitelist convention. The repo scope itself is applied by the
// caller via FilterSpec.WithRepo, not by this function.
func parseChangesetsFilter(q url.Values, knownFacets map[string][]string) (filter.FilterSpec, error) {
	allowed := make(map[string]struct{}, len(knownFacets))
	params := make(map[string][]string, len(q))

	for name := range knownFacets {
		if isReservedChangesetsParam(name) {
			continue
		}
		allowed[name] = struct{}{}
		if vals, present := q[name]; present {
			params[name] = vals
		}
	}

	return filter.Parse(params, allowed)
}

// isReservedChangesetsParam reports whether name is a reserved query-param
// name (asOf, cursor, limit, repo) and therefore never eligible as a facet
// filter.
func isReservedChangesetsParam(name string) bool {
	_, reserved := reservedChangesetsParams[name]
	return reserved
}

// parseLimit parses the limit query param and clamps it to
// maxChangesetPageSize — the endpoint never passes a caller-dictated limit
// straight through to the store. An empty string yields
// defaultChangesetPageSize. A non-positive or non-integer value is rejected
// as a malformed request.
func parseLimit(raw string) (int, error) {
	if raw == "" {
		return defaultChangesetPageSize, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, fmt.Errorf("web: limit must be positive, got %d", n)
	}
	if n > maxChangesetPageSize {
		return maxChangesetPageSize, nil
	}
	return n, nil
}

// parseAsOf parses the asOf query param as RFC3339. An empty string defaults
// to "now" — the sensible default for "show me the current state of the
// world" when the caller does not pin a point in time.
func parseAsOf(raw string) (time.Time, error) {
	if raw == "" {
		return time.Now(), nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

// parseSince parses the since query param as RFC3339 — the same format asOf
// accepts, so a caller never has to learn a second time format for the same
// endpoint. An empty string yields the zero Time, which store.TimeWindow
// reads as "no lower bound": omitting since leaves the endpoint behaving
// exactly as it did before the param existed.
//
// A since at or after asOf is deliberately not rejected here. It describes an
// empty window, which the store answers with an empty page — a normal outcome
// for a polling loop, not a caller error.
func parseSince(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

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

// writeJSON marshals v and writes it with the given status code.
func writeJSON(r *http.Request, w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		telemetry.LoggerFromContext(r.Context()).Error("web: marshal changesets response", "error", err)
		http.Error(w, genericServerErrorMsg, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		// The status code is already on the wire, so there is nothing to do but
		// record it — previously discarded outright, which hid genuine write
		// failures alongside the harmless client-disconnect ones.
		logResponseWriteError(r.Context(), "web: write changesets response", err)
	}
}
