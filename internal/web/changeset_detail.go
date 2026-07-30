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
func (h *ChangesetDetailHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	repo := r.URL.Query().Get("repo")
	commitSha := r.URL.Query().Get("commitSha")
	if repo == "" || commitSha == "" {
		http.Error(w, genericBadRequestMsg, http.StatusBadRequest)
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
		http.Error(w, genericServerErrorMsg, http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	if err := renderChangesetDetail(w, cs, riskRulesOrDefault(h.risk)); err != nil {
		logResponseWriteError(r.Context(), "web: render changeset detail", err)
	}
}
