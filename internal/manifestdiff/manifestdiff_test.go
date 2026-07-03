package manifestdiff_test

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/manifestdiff"
)

// assertEveryLineIsPrefixed fails the test if any non-empty physical line in
// unified (splitting on "\n") does not start with exactly one of "+", "-",
// or " ". It is the shared guard for the "every diff line is
// newline-terminated at emission, so no two lines can ever fuse onto one
// physical line" invariant, used across every shape of input that can
// exercise it (a changed pair, an added-only manifest, a removed-only
// manifest, and manifests lacking a trailing newline in their YAML).
func assertEveryLineIsPrefixed(t *testing.T, unified string) {
	t.Helper()

	trimmed := strings.TrimSuffix(unified, "\n")
	if trimmed == "" {
		return
	}
	for _, line := range strings.Split(trimmed, "\n") {
		if line == "" {
			continue
		}
		if c := line[0]; c != '+' && c != '-' && c != ' ' {
			t.Errorf("line %q does not start with +, -, or a space (fused or unterminated line); full Unified:\n%s", line, unified)
		}
	}
}

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

// TestDiff_UnequalManifestCounts_NoSeparatorInflation is a regression test
// for a CRITICAL bug: an earlier implementation concatenated each side's
// manifests with a literal "---\n" separator and line-diffed the two
// concatenated blobs. When a manifest was added or removed alongside other,
// unrelated manifests, the number of separators differed between Old and
// New, so a separator line itself was miscounted as a spurious +/- line —
// inflating LinesRemoved/LinesAdded by 1 per net add/remove and injecting a
// fabricated "----" line into Unified. Pairing and diffing per identity (with
// no separator token at all) must never do this.
func TestDiff_UnequalManifestCounts_NoSeparatorInflation(t *testing.T) {
	t.Parallel()

	steady := manifestdiff.Manifest{
		Kind: "ConfigMap", Namespace: "default", Name: "steady",
		YAML: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: steady\ndata:\n  x: \"1\"\n",
	}
	// removed has exactly 4 real YAML lines.
	removed := manifestdiff.Manifest{
		Kind: "ConfigMap", Namespace: "default", Name: "removed-cm",
		YAML: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: removed-cm\n",
	}
	// added has exactly 4 real YAML lines.
	added := manifestdiff.Manifest{
		Kind: "ConfigMap", Namespace: "default", Name: "added-cm",
		YAML: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: added-cm\n",
	}
	const realLineCount = 4

	t.Run("removal alongside a coexisting manifest", func(t *testing.T) {
		t.Parallel()

		result := manifestdiff.Diff(manifestdiff.Params{
			Old: []manifestdiff.Manifest{steady, removed}, // 2 manifests
			New: []manifestdiff.Manifest{steady},          // 1 manifest
		})

		if result.Summary.LinesRemoved != realLineCount {
			t.Errorf("Summary.LinesRemoved = %d, want %d (exactly removed-cm's lines, no separator inflation)",
				result.Summary.LinesRemoved, realLineCount)
		}
		if result.Summary.LinesAdded != 0 {
			t.Errorf("Summary.LinesAdded = %d, want 0", result.Summary.LinesAdded)
		}
		if strings.Contains(result.Unified, "----") {
			t.Errorf("Unified contains a fabricated separator line, got:\n%s", result.Unified)
		}
	})

	t.Run("addition alongside a coexisting manifest", func(t *testing.T) {
		t.Parallel()

		result := manifestdiff.Diff(manifestdiff.Params{
			Old: []manifestdiff.Manifest{steady},        // 1 manifest
			New: []manifestdiff.Manifest{steady, added}, // 2 manifests
		})

		if result.Summary.LinesAdded != realLineCount {
			t.Errorf("Summary.LinesAdded = %d, want %d (exactly added-cm's lines, no separator inflation)",
				result.Summary.LinesAdded, realLineCount)
		}
		if result.Summary.LinesRemoved != 0 {
			t.Errorf("Summary.LinesRemoved = %d, want 0", result.Summary.LinesRemoved)
		}
		if strings.Contains(result.Unified, "----") {
			t.Errorf("Unified contains a fabricated separator line, got:\n%s", result.Unified)
		}
	})
}

// TestDiff_TruncateFallback_NeverSplitsAMultibyteRune is a regression test
// for a MEDIUM bug: when the truncation ceiling lands inside a single line
// with no earlier newline to back up to (truncateAtLineBoundary's hard-cut
// fallback), a raw byte cut could split a multi-byte UTF-8 rune and yield
// invalid UTF-8. Diff must always back the cut up to a valid rune boundary.
func TestDiff_TruncateFallback_NeverSplitsAMultibyteRune(t *testing.T) {
	t.Parallel()

	// "aé" (3 bytes: 'a', then 'é' as two UTF-8 bytes) with no trailing
	// newline is a single line, so the rendered "+"-prefixed block is
	// "+aé" (4 bytes) with no newline anywhere for truncateAtLineBoundary to
	// back up to — forcing the hard-cut fallback path.
	solo := manifestdiff.Manifest{Kind: "ConfigMap", Namespace: "default", Name: "solo", YAML: "aé"}

	// A 3-byte ceiling cuts "+aé" right after the first byte of 'é'
	// (0xC3), which is not a valid UTF-8 boundary on its own.
	const tinyCeiling = 3

	result := manifestdiff.Diff(manifestdiff.Params{
		New:             []manifestdiff.Manifest{solo},
		MaxUnifiedBytes: tinyCeiling,
	})

	if !result.Truncated {
		t.Fatalf("Truncated = false, want true")
	}
	if !utf8.ValidString(result.Unified) {
		t.Errorf("Unified is not valid UTF-8: %q (bytes: % x)", result.Unified, result.Unified)
	}
	if want := "+a"; result.Unified != want {
		t.Errorf("Unified = %q, want %q (backed up past the split rune)", result.Unified, want)
	}
}

// TestDiff_BlockWithoutTrailingNewline_DoesNotMergeIntoNextManifest is a
// regression test for a CRITICAL bug: renderPairs concatenated each
// per-identity block directly, so when an earlier-sorted manifest's YAML
// lacked a trailing newline, the next manifest's content was appended onto
// the same physical line — a consumer splitting Unified on "\n" would see a
// line with no leading +/-/space marker, and two unrelated manifests glued
// together. Manifest.YAML is unvalidated caller input, so this must be
// handled regardless of whether real chartrender output always terminates
// its YAML (it does; but Diff cannot assume every caller does).
func TestDiff_BlockWithoutTrailingNewline_DoesNotMergeIntoNextManifest(t *testing.T) {
	t.Parallel()

	// "a" sorts before "b"; "a"'s YAML deliberately has no trailing newline.
	first := manifestdiff.Manifest{
		Kind: "ConfigMap", Namespace: "default", Name: "a",
		YAML: "line-one-no-newline",
	}
	second := manifestdiff.Manifest{
		Kind: "ConfigMap", Namespace: "default", Name: "b",
		YAML: "line-two\n",
	}

	result := manifestdiff.Diff(manifestdiff.Params{
		New: []manifestdiff.Manifest{first, second},
	})

	const want = "+line-one-no-newline\n+line-two\n"
	if result.Unified != want {
		t.Fatalf("Unified = %q, want %q (manifests must not merge onto one line)", result.Unified, want)
	}

	assertEveryLineIsPrefixed(t, result.Unified)

	if result.Summary.LinesAdded != 2 {
		t.Errorf("Summary.LinesAdded = %d, want 2 (the raw newline terminator must not be counted)", result.Summary.LinesAdded)
	}
	if result.Summary.LinesRemoved != 0 {
		t.Errorf("Summary.LinesRemoved = %d, want 0", result.Summary.LinesRemoved)
	}
	if result.Summary.ManifestsChanged != 2 {
		t.Errorf("Summary.ManifestsChanged = %d, want 2", result.Summary.ManifestsChanged)
	}
}

// TestDiff_ChangedPair_NonTerminatedYAML_DoesNotFuseDiffLines is a
// regression test for a CRITICAL bug one level deeper than the
// inter-manifest gluing above: renderUnified concatenated diffmatchpatch's
// rendered lines back-to-back. Every line token carries its own trailing
// "\n" except the final token of whichever side (old or new) lacks a
// trailing newline — and diffmatchpatch orders ops delete-before-insert, so
// that unterminated token is often NOT the last op rendered, letting the
// next op's text glue onto the same physical line (e.g.
// "-line1+line1\n+line2\n" fusing a "-" onto a "+"). This must hold in
// either direction: whichever side lacks the trailing newline.
func TestDiff_ChangedPair_NonTerminatedYAML_DoesNotFuseDiffLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		oldYAML string
		newYAML string
		want    string
	}{
		{
			name:    "old side lacks trailing newline",
			oldYAML: "line1",
			newYAML: "line1\nline2",
			want:    "-line1\n+line1\n+line2\n",
		},
		{
			name:    "new side lacks trailing newline",
			oldYAML: "line1\nline2",
			newYAML: "line1",
			want:    "-line1\n-line2\n+line1\n",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := manifestdiff.Diff(manifestdiff.Params{
				Old: []manifestdiff.Manifest{{Kind: "ConfigMap", Namespace: "default", Name: "changed", YAML: tc.oldYAML}},
				New: []manifestdiff.Manifest{{Kind: "ConfigMap", Namespace: "default", Name: "changed", YAML: tc.newYAML}},
			})

			assertEveryLineIsPrefixed(t, result.Unified)

			if result.Unified != tc.want {
				t.Errorf("Unified = %q, want %q (a delete/insert op boundary must not fuse lines)", result.Unified, tc.want)
			}
		})
	}
}

// TestDiff_MixedManifests_EveryPhysicalLineIsPrefixed is a universal
// invariant test for the whole newline-termination bug class, rather than
// any one reported input: a single fixture exercises all three renderPairs
// branches at once — a changed pair, an added-only manifest, and a
// removed-only manifest — with at least one manifest's YAML lacking a
// trailing newline, and asserts every physical line in Unified still starts
// with exactly one of "+", "-", or " ".
func TestDiff_MixedManifests_EveryPhysicalLineIsPrefixed(t *testing.T) {
	t.Parallel()

	oldChanged := manifestdiff.Manifest{Kind: "ConfigMap", Namespace: "default", Name: "changed", YAML: "old-no-newline"}
	newChanged := manifestdiff.Manifest{Kind: "ConfigMap", Namespace: "default", Name: "changed", YAML: "new-line-one\nnew-line-two"}
	removedOnly := manifestdiff.Manifest{Kind: "Service", Namespace: "default", Name: "removed-svc", YAML: "removed-no-newline"}
	addedOnly := manifestdiff.Manifest{Kind: "Service", Namespace: "default", Name: "added-svc", YAML: "added-line\n"}

	result := manifestdiff.Diff(manifestdiff.Params{
		Old: []manifestdiff.Manifest{oldChanged, removedOnly},
		New: []manifestdiff.Manifest{newChanged, addedOnly},
	})

	assertEveryLineIsPrefixed(t, result.Unified)

	if result.Summary.ManifestsChanged != 3 {
		t.Errorf("Summary.ManifestsChanged = %d, want 3 (1 changed, 1 added, 1 removed)", result.Summary.ManifestsChanged)
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
