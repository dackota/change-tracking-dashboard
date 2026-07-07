// Package web (this file): the GET /healthz liveness route (R13) — a
// trivial, dependency-free handler suitable for a Kubernetes liveness probe.
// It never checks the store, config watcher, or poll status: liveness only
// answers "is this process's HTTP server still able to handle a request,"
// not "is everything downstream healthy" — readiness semantics are out of
// scope per the PRD.
package web

import (
	"log"
	"net/http"
)

// HealthzHandler serves GET /healthz: always 200, no dependency checks.
type HealthzHandler struct{}

// NewHealthzHandler returns a ready-to-use HealthzHandler. It takes no
// dependencies — liveness never touches the store, config, or poll status.
func NewHealthzHandler() *HealthzHandler {
	return &HealthzHandler{}
}

// ServeHTTP satisfies http.Handler.
func (h *HealthzHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("ok")); err != nil {
		log.Printf("web: write healthz response: %v", err)
	}
}
