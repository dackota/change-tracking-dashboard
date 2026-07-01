// Package web (this file): the GET /api/changesets/detail endpoint. It
// renders the full detail view for a single Changeset — every Change that
// commit produced, dispatched to a per-kind rendering (value vs chart) —
// as server-rendered HTML via html/template (auto-escaping). This is the
// server-side seam the vendored timeline.js's onFlagClick hooks: the
// per-kind dispatch/rendering logic lives and is tested here, not in
// client-side JS.
package web

import (
	"log"
	"net/http"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/store"
)

// ChangesetDetailHandler serves GET /api/changesets/detail as rendered HTML.
type ChangesetDetailHandler struct {
	st *store.Store
}

// NewChangesetDetailHandler creates a ChangesetDetailHandler backed by the
// given store.
func NewChangesetDetailHandler(st *store.Store) *ChangesetDetailHandler {
	return &ChangesetDetailHandler{st: st}
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

	cs, found, err := h.st.GetChangeset(repo, commitSha)
	if err != nil {
		log.Printf("web: get changeset detail: %v", err)
		http.Error(w, genericServerErrorMsg, http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	if err := renderChangesetDetail(w, cs); err != nil {
		log.Printf("web: render changeset detail: %v", err)
	}
}
