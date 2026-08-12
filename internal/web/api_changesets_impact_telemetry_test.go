package web_test

import (
	"fmt"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/web"
	"go.opentelemetry.io/otel/attribute"
	tracetest "go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// spanIntAttr returns the int64 value of key on the first span named name.
func spanIntAttr(t *testing.T, spans tracetest.SpanStubs, name string, key attribute.Key) int64 {
	t.Helper()

	for _, s := range spans {
		if s.Name != name {
			continue
		}
		for _, kv := range s.Attributes {
			if kv.Key == key {
				return kv.Value.AsInt64()
			}
		}
		t.Fatalf("span %q has no attribute %q; attributes: %v", name, key, s.Attributes)
	}
	t.Fatalf("no span named %q recorded; spans: %v", name, spans)
	return 0
}

// TestChangesetsAPI_ImpactFilterRecordsScanCountsOnSpan verifies the
// page-fill loop's work is visible in a trace: the store.query_changesets span
// carries how many commits were examined alongside how many were returned.
// Without both numbers a pathological filter — one that scans thousands of
// commits to return three — is indistinguishable in a trace from a cheap
// query that returned three, which is exactly the failure mode the scan
// budget exists to bound and an operator needs to be able to see.
//
// Like TestChangesetsHandler_QueryChangesets_WrappedInSpan this test
// deliberately does NOT run in parallel: it reads the process-wide
// spanExporter that TestMain installs, resetting it first to isolate this
// assertion from any earlier test's spans.
func TestChangesetsAPI_ImpactFilterRecordsScanCountsOnSpan(t *testing.T) {
	spanExporter.Reset()

	st := newTestStore(t)
	// 30 commits, every third a major bump: 10 matches among 30 examined, so
	// examined and returned are distinguishable numbers rather than
	// coincidentally equal.
	for i := range 30 {
		newVal := "1.0.1"
		if i%3 == 0 {
			newVal = "2.0.0"
		}
		seedTieredCommit(t, st, fmt.Sprintf("commit-%02d", i), "1.0.0", newVal, "infra-repo", nil, i)
	}

	h := web.NewChangesetsHandler(st)

	got := changesetSHAs(t, getChangesets(t, h, "?impact=major&limit=5"))
	if len(got) != 5 {
		t.Fatalf("got %d changesets, want a full page of 5", len(got))
	}

	spans := spanExporter.GetSpans()
	examined := spanIntAttr(t, spans, "store.query_changesets", "changesets.examined")
	returned := spanIntAttr(t, spans, "store.query_changesets", "changesets.returned")

	if returned != 5 {
		t.Errorf("changesets.returned = %d, want 5", returned)
	}
	// Filling a 5-changeset page at 1-in-3 selectivity requires examining
	// well more than 5 commits — the exact count depends on batch sizing, so
	// assert the relationship the attribute exists to expose, not a magic
	// number that would break on any tuning change.
	if examined <= returned {
		t.Errorf("changesets.examined = %d, want strictly greater than returned (%d) under a 1-in-3 filter", examined, returned)
	}
	if examined > 30 {
		t.Errorf("changesets.examined = %d, but only 30 commits exist", examined)
	}
}
