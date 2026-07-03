package manifestdiff_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/manifestdiff"
)

// TestDiff_KnownChange_ProducesUnifiedDiffAndSummary proves the core
// behavior: two manifest sets that share one unchanged ConfigMap and one
// ConfigMap whose YAML differs produce a unified +/- diff for the changed
// manifest (with the unchanged one appearing only as diff-free context) plus
// a summary that counts exactly one changed manifest and the true added/
// removed line counts.
func TestDiff_KnownChange_ProducesUnifiedDiffAndSummary(t *testing.T) {
	t.Parallel()

	unchanged := manifestdiff.Manifest{
		Kind: "ConfigMap", Namespace: "default", Name: "steady",
		YAML: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: steady\ndata:\n  x: \"1\"\n",
	}
	oldChanged := manifestdiff.Manifest{
		Kind: "ConfigMap", Namespace: "default", Name: "moved",
		YAML: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: moved\ndata:\n  replicas: \"1\"\n",
	}
	newChanged := manifestdiff.Manifest{
		Kind: "ConfigMap", Namespace: "default", Name: "moved",
		YAML: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: moved\ndata:\n  replicas: \"2\"\n",
	}

	result := manifestdiff.Diff(manifestdiff.Params{
		Old: []manifestdiff.Manifest{unchanged, oldChanged},
		New: []manifestdiff.Manifest{unchanged, newChanged},
	})

	if !strings.Contains(result.Unified, "-  replicas: \"1\"\n") {
		t.Errorf("Unified missing removed line, got:\n%s", result.Unified)
	}
	if !strings.Contains(result.Unified, "+  replicas: \"2\"\n") {
		t.Errorf("Unified missing added line, got:\n%s", result.Unified)
	}
	if strings.Contains(result.Unified, "+kind: ConfigMap\n") || strings.Contains(result.Unified, "-kind: ConfigMap\n") {
		t.Errorf("Unified should show unchanged lines as context, not +/-, got:\n%s", result.Unified)
	}

	if result.Summary.ManifestsChanged != 1 {
		t.Errorf("Summary.ManifestsChanged = %d, want 1", result.Summary.ManifestsChanged)
	}
	if result.Summary.LinesAdded != 1 {
		t.Errorf("Summary.LinesAdded = %d, want 1", result.Summary.LinesAdded)
	}
	if result.Summary.LinesRemoved != 1 {
		t.Errorf("Summary.LinesRemoved = %d, want 1", result.Summary.LinesRemoved)
	}
	if result.Truncated {
		t.Errorf("Truncated = true, want false")
	}
}

// TestDiff_ReorderedButEqual_ProducesNoSpuriousDiff proves identity-based
// pairing: the same manifests supplied to Old and New in a different input
// order produce no diff at all, because Diff sorts each side by identity
// before comparing rather than trusting positional order.
func TestDiff_ReorderedButEqual_ProducesNoSpuriousDiff(t *testing.T) {
	t.Parallel()

	configMap := manifestdiff.Manifest{
		Kind: "ConfigMap", Namespace: "default", Name: "steady",
		YAML: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: steady\ndata:\n  x: \"1\"\n",
	}
	service := manifestdiff.Manifest{
		Kind: "Service", Namespace: "default", Name: "svc",
		YAML: "apiVersion: v1\nkind: Service\nmetadata:\n  name: svc\n",
	}

	result := manifestdiff.Diff(manifestdiff.Params{
		Old: []manifestdiff.Manifest{configMap, service}, // Kind order: ConfigMap, Service
		New: []manifestdiff.Manifest{service, configMap}, // Kind order: Service, ConfigMap (reversed)
	})

	if result.Unified != "" {
		t.Errorf("Unified = %q, want empty (reordered-but-equal must not diff)", result.Unified)
	}
	if result.Summary != (manifestdiff.Summary{}) {
		t.Errorf("Summary = %+v, want zero value", result.Summary)
	}
}

// TestDiff_AddedAndRemovedManifests_ReflectedInSummaryAndDiff proves that a
// manifest present only in New is an addition and a manifest present only in
// Old is a removal — both counted in ManifestsChanged and rendered as
// pure +/- blocks (no context, since there's no shared identity to align
// against).
func TestDiff_AddedAndRemovedManifests_ReflectedInSummaryAndDiff(t *testing.T) {
	t.Parallel()

	steady := manifestdiff.Manifest{
		Kind: "ConfigMap", Namespace: "default", Name: "steady",
		YAML: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: steady\ndata:\n  x: \"1\"\n",
	}
	removedOnly := manifestdiff.Manifest{
		Kind: "Service", Namespace: "default", Name: "old-svc",
		YAML: "apiVersion: v1\nkind: Service\nmetadata:\n  name: old-svc\n",
	}
	addedOnly := manifestdiff.Manifest{
		Kind: "Service", Namespace: "default", Name: "new-svc",
		YAML: "apiVersion: v1\nkind: Service\nmetadata:\n  name: new-svc\n",
	}

	result := manifestdiff.Diff(manifestdiff.Params{
		Old: []manifestdiff.Manifest{steady, removedOnly},
		New: []manifestdiff.Manifest{steady, addedOnly},
	})

	if result.Summary.ManifestsChanged != 2 {
		t.Errorf("Summary.ManifestsChanged = %d, want 2 (one added, one removed)", result.Summary.ManifestsChanged)
	}
	if !strings.Contains(result.Unified, "-  name: old-svc\n") {
		t.Errorf("Unified missing removed manifest's line, got:\n%s", result.Unified)
	}
	if !strings.Contains(result.Unified, "+  name: new-svc\n") {
		t.Errorf("Unified missing added manifest's line, got:\n%s", result.Unified)
	}
}

// TestDiff_OversizedInput_TruncatesButKeepsTrueSummaryTotals proves user
// story 8: when the rendered unified diff would exceed a small configured
// MaxUnifiedBytes, Diff sets Truncated and cuts Unified down to the ceiling
// (at a line boundary, never mid-line), while Summary still reports the full,
// untruncated totals — an honest blast-radius even when the shown diff is
// cut.
func TestDiff_OversizedInput_TruncatesButKeepsTrueSummaryTotals(t *testing.T) {
	t.Parallel()

	// 50 manifests, each with a one-line YAML body that changes between Old
	// and New, produces a diff far larger than the tiny ceiling below.
	const manifestCount = 50
	old := make([]manifestdiff.Manifest, manifestCount)
	new := make([]manifestdiff.Manifest, manifestCount)
	for i := 0; i < manifestCount; i++ {
		name := fmt.Sprintf("cm-%02d", i)
		old[i] = manifestdiff.Manifest{
			Kind: "ConfigMap", Namespace: "default", Name: name,
			YAML: fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\ndata:\n  version: \"old-%d\"\n", name, i),
		}
		new[i] = manifestdiff.Manifest{
			Kind: "ConfigMap", Namespace: "default", Name: name,
			YAML: fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\ndata:\n  version: \"new-%d\"\n", name, i),
		}
	}

	const tinyCeiling = 200 // bytes — far smaller than the full diff

	full := manifestdiff.Diff(manifestdiff.Params{Old: old, New: new})
	truncatedResult := manifestdiff.Diff(manifestdiff.Params{Old: old, New: new, MaxUnifiedBytes: tinyCeiling})

	if !truncatedResult.Truncated {
		t.Fatalf("Truncated = false, want true for a %d-byte ceiling", tinyCeiling)
	}
	if len(truncatedResult.Unified) > tinyCeiling {
		t.Errorf("len(Unified) = %d, want <= %d", len(truncatedResult.Unified), tinyCeiling)
	}
	if strings.HasSuffix(truncatedResult.Unified, "\n") == false && truncatedResult.Unified != "" {
		t.Errorf("Unified must be cut at a line boundary, got tail: %q", truncatedResult.Unified[len(truncatedResult.Unified)-10:])
	}

	if truncatedResult.Summary != full.Summary {
		t.Errorf("truncated Summary = %+v, want it to match the untruncated Summary %+v (true totals)", truncatedResult.Summary, full.Summary)
	}
	if truncatedResult.Summary.ManifestsChanged != manifestCount {
		t.Errorf("Summary.ManifestsChanged = %d, want %d", truncatedResult.Summary.ManifestsChanged, manifestCount)
	}

	if full.Truncated {
		t.Errorf("full-ceiling Result.Truncated = true, want false")
	}
}

// TestDiff_IdenticalSets_ProducesEmptyDiffAndZeroSummary proves the base
// case: comparing a manifest set against an identical copy of itself yields
// no diff output and an all-zero summary.
func TestDiff_IdenticalSets_ProducesEmptyDiffAndZeroSummary(t *testing.T) {
	t.Parallel()

	manifests := []manifestdiff.Manifest{
		{
			Kind: "ConfigMap", Namespace: "default", Name: "steady",
			YAML: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: steady\ndata:\n  x: \"1\"\n",
		},
		{
			Kind: "Service", Namespace: "default", Name: "svc",
			YAML: "apiVersion: v1\nkind: Service\nmetadata:\n  name: svc\n",
		},
	}

	result := manifestdiff.Diff(manifestdiff.Params{
		Old: manifests,
		New: manifests,
	})

	if result.Unified != "" {
		t.Errorf("Unified = %q, want empty", result.Unified)
	}
	if result.Summary != (manifestdiff.Summary{}) {
		t.Errorf("Summary = %+v, want zero value", result.Summary)
	}
	if result.Truncated {
		t.Errorf("Truncated = true, want false")
	}
}
