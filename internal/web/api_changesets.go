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
	"limit":  {},
	"repo":   {},
}

// ChangesetsHandler serves GET /api/changesets as JSON.
type ChangesetsHandler struct {
	st *store.Store
}

// NewChangesetsHandler creates a ChangesetsHandler backed by the given store.
func NewChangesetsHandler(st *store.Store) *ChangesetsHandler {
	return &ChangesetsHandler{st: st}
}

// changesetsResponse is the top-level JSON response body.
type changesetsResponse struct {
	Changesets []changesetJSON `json:"changesets"`
	NextCursor string          `json:"nextCursor"`
}

// changesetJSON is the explicit JSON shape for one Changeset. Defined here
// (rather than relying on changeset.Changeset's Go struct tags) so the wire
// format is decoupled from internal field naming.
type changesetJSON struct {
	Repo        string       `json:"repo"`
	CommitSha   string       `json:"commitSha"`
	Author      string       `json:"author"`
	CommittedAt string       `json:"committedAt"`
	IssueRefs   []string     `json:"issueRefs,omitempty"`
	Changes     []changeJSON `json:"changes"`
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

	cursor := r.URL.Query().Get("cursor")

	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		logger.Error("web: parse limit", "error", err)
		http.Error(w, genericBadRequestMsg, http.StatusBadRequest)
		return
	}

	var page store.ChangesetPage
	err = telemetry.WithSpan(r.Context(), tracer, "store.query_changesets", func(context.Context) error {
		var err error
		page, err = h.st.QueryChangesets(asOf, spec, cursor, limit)
		return err
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
		Changesets: toChangesetsJSON(page.Changesets),
		NextCursor: page.NextCursor,
	}
	writeJSON(r, w, http.StatusOK, resp)
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

// toChangesetsJSON converts a slice of changeset.Changeset to their explicit
// JSON shape.
func toChangesetsJSON(sets []changeset.Changeset) []changesetJSON {
	out := make([]changesetJSON, 0, len(sets))
	for _, cs := range sets {
		out = append(out, toChangesetJSON(cs))
	}
	return out
}

// toChangesetJSON converts a single changeset.Changeset to its explicit JSON
// shape.
func toChangesetJSON(cs changeset.Changeset) changesetJSON {
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
		Changes:     changes,
	}
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
	_, _ = w.Write(body)
}
