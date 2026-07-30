// Package web (this file): the GET /healthz liveness route (R13) — a
// trivial, dependency-free handler suitable for a Kubernetes liveness probe.
// It never checks the store, config watcher, or poll status: liveness only
// answers "is this process's HTTP server still able to handle a request,"
// not "is everything downstream healthy" — readiness semantics are out of
// scope per the PRD.
package web

import "net/http"

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
		// Probes are the overwhelming majority of this route's traffic and
		// kubelet closes the connection as soon as it has the status code, so
		// a failed body write here is nearly always the prober hanging up —
		// not something to page on.
		logResponseWriteError(r.Context(), "web: write healthz response", err)
	}
}
