// Package web (this file): the GET /api/changesets/detail endpoint. It
// renders the full detail view for a single Changeset — every Change that
// commit produced, dispatched to a per-kind rendering (value vs chart) —
// as server-rendered HTML via html/template (auto-escaping). This is the
// server-side seam the vendored timeline.js's onFlagClick hooks: the
// per-kind dispatch/rendering logic lives and is tested here, not in
// client-side JS.
package web

import (
	"context"
	"net/http"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/store"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
)

// ChangesetDetailHandler serves GET /api/changesets/detail as rendered HTML.
type ChangesetDetailHandler struct {
	st   *store.Store
	risk RiskRulesSource
}

// ChangesetDetailOption configures a ChangesetDetailHandler at construction.
type ChangesetDetailOption func(*ChangesetDetailHandler)

// WithDetailRiskRules sets the source of risk rules the detail view classifies
// its changeset against. When unset, the handler falls back to the built-in
// changeset.DefaultRiskRules (see riskRulesOrDefault).
func WithDetailRiskRules(src RiskRulesSource) ChangesetDetailOption {
	return func(h *ChangesetDetailHandler) { h.risk = src }
}

// NewChangesetDetailHandler creates a ChangesetDetailHandler backed by the
// given store.
func NewChangesetDetailHandler(st *store.Store, opts ...ChangesetDetailOption) *ChangesetDetailHandler {
	h := &ChangesetDetailHandler{st: st}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// ServeHTTP satisfies http.Handler.
//
// One set of parameter parsing, one store lookup, and one existence gate serve
// both representations; the handler branches only at the final render step.
// The gates are deliberately not duplicated per representation — a security or
// existence check written twice is a check that will eventually be fixed once,
// and the two representations would then disagree about what exists.
func (h *ChangesetDetailHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Security headers apply to every response regardless of representation or
	// status, so they are set before anything can return.
	setSecurityHeaders(w.Header())

	// Negotiation depends only on the Accept header, so it is decided up front
	// and every exit path below — including the error paths — honors it.
	// Content-Type is deliberately NOT set here: it is set by whichever
	// renderer actually runs, so it can never describe a body that was not
	// produced.
	json := wantsJSON(r.Header.Get("Accept"))

	repo := r.URL.Query().Get("repo")
	commitSha := r.URL.Query().Get("commitSha")
	if repo == "" || commitSha == "" {
		writeDetailError(r, w, json, http.StatusBadRequest, genericBadRequestMsg)
		return
	}

	logger := telemetry.LoggerFromContext(r.Context())

	var cs changeset.Changeset
	var found bool
	err := telemetry.WithSpan(r.Context(), tracer, "store.get_changeset", func(context.Context) error {
		var err error
		cs, found, err = h.st.GetChangeset(repo, commitSha)
		return err
	})
	if err != nil {
		logger.Error("web: get changeset detail", "error", err)
		writeDetailError(r, w, json, http.StatusInternalServerError, genericServerErrorMsg)
		return
	}
	if !found {
		// The existence gate runs before any representation is chosen, so an
		// unknown changeset is a 404 in both forms with no extra distinguishing
		// signal in either — a caller cannot probe for which commits exist by
		// switching representations.
		writeDetailError(r, w, json, http.StatusNotFound, genericNotFoundMsg)
		return
	}

	rules := riskRulesOrDefault(h.risk)

	if json {
		// The same wire shape the list endpoint emits, including the computed
		// risk[] and impact projections, so a client parses one changeset type
		// everywhere rather than a detail-specific variant.
		writeJSON(r, w, http.StatusOK, toChangesetJSON(cs, rules))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := renderChangesetDetail(w, cs, rules); err != nil {
		logResponseWriteError(r.Context(), "web: render changeset detail", err)
	}
}

// genericNotFoundMsg is the only text sent for an unknown changeset. It says
// nothing about whether the repo exists, the commit exists, or neither.
const genericNotFoundMsg = "not found"

// errorJSON is the wire shape for an error when JSON was negotiated, so a
// client parses every response — success or failure — with one code path.
// The message is always one of the package's generic constants; caller input
// is never interpolated into it.
type errorJSON struct {
	Error string `json:"error"`
}

// writeDetailError responds with status and a generic message in whichever
// representation was negotiated. The HTML form keeps http.Error's existing
// plain-text body byte-for-byte, so non-JSON clients see exactly what they
// saw before negotiation existed.
func writeDetailError(r *http.Request, w http.ResponseWriter, asJSON bool, status int, msg string) {
	if asJSON {
		writeJSON(r, w, status, errorJSON{Error: msg})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	http.Error(w, msg, status)
}
