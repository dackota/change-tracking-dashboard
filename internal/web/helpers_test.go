package web_test

import (
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/store"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/web"
)

// newTestHandler constructs a web.Handler backed by the given store. It is
// provided as a shared helper so all filter tests use the same construction path.
func newTestHandler(t *testing.T, st *store.Store) *web.Handler {
	t.Helper()
	return web.NewHandler(st)
}
