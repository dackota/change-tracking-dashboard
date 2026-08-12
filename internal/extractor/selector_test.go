package extractor_test

import (
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/extractor"
)

// TestSelect_LegalEngineValues covers the legal-value contract: unset/empty,
// "jq", and "hcl" (the HCL/Terraform backend — see internal/hclextract) are
// the values accepted today; anything else is rejected so a typo'd config
// fails fast. Engine legality is only reachable through Select — there is no
// separate validator to call, and so no way to check the engine but forget to
// compile with it.
func TestSelect_LegalEngineValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		engine  string
		wantErr bool
	}{
		{name: "empty defaults to jq", engine: "", wantErr: false},
		{name: "explicit jq", engine: "jq", wantErr: false},
		{name: "explicit hcl", engine: "hcl", wantErr: false},
		{name: "unknown value", engine: "bogus", wantErr: true},
		{name: "case mismatch is rejected", engine: "JQ", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// A trivially valid expression in either engine, so only the
			// engine value decides the outcome.
			_, err := extractor.Select(tc.engine, "Chart.yaml", "version")
			if tc.wantErr && err == nil {
				t.Fatalf("Select(%q, ...) = nil, want an error", tc.engine)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Select(%q, ...) = %v, want nil", tc.engine, err)
			}
		})
	}
}

// TestSelect_BadEngineErrorNamesTheBadValue verifies the error is actionable —
// it must name the invalid value so a human can find the typo in their config.
func TestSelect_BadEngineErrorNamesTheBadValue(t *testing.T) {
	t.Parallel()

	_, err := extractor.Select("cobol", "Chart.yaml", ".version")
	if err == nil {
		t.Fatal("Select(\"cobol\", ...) = nil, want an error")
	}
	if !contains(err.Error(), "cobol") {
		t.Errorf("error %q does not name the invalid value %q", err.Error(), "cobol")
	}
}

// TestSelect_ResolvesEngine covers the whole resolution table in one place:
// an explicit engine wins regardless of the glob, and an empty engine is
// inferred from the glob's suffix, defaulting to jq. Resolution is not
// separately reachable — Select is the only way to ask — so this asserts it
// through the Selection's own Engine().
func TestSelect_ResolvesEngine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		engine string
		glob   string
		expr   string
		want   string
	}{
		{name: "empty engine, no glob, defaults to jq", engine: "", glob: "", expr: ".version", want: "jq"},
		{name: "empty engine, yaml glob, defaults to jq", engine: "", glob: "Chart.yaml", expr: ".version", want: "jq"},
		{name: "empty engine, .tf glob infers hcl", engine: "", glob: "versions.tf", expr: "terraform.required_version", want: "hcl"},
		{name: "empty engine, .tofu glob infers hcl", engine: "", glob: "main.tofu", expr: "terraform.required_version", want: "hcl"},
		{name: "empty engine, lockfile glob infers hcl", engine: "", glob: ".terraform.lock.hcl", expr: `provider["x"].version`, want: "hcl"},
		{name: "empty engine, glob path with .tf suffix infers hcl", engine: "", glob: "infra/**/versions.tf", expr: "terraform.required_version", want: "hcl"},
		{name: "explicit jq wins over an hcl-looking glob", engine: "jq", glob: "versions.tf", expr: ".version", want: "jq"},
		{name: "explicit hcl wins over a jq-looking glob", engine: "hcl", glob: "Chart.yaml", expr: "terraform.required_version", want: "hcl"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sel, err := extractor.Select(tc.engine, tc.glob, tc.expr)
			if err != nil {
				t.Fatalf("Select(%q, %q, %q): unexpected error: %v", tc.engine, tc.glob, tc.expr, err)
			}
			if got := sel.Engine(); got != tc.want {
				t.Errorf("Select(%q, %q, ...).Engine() = %q, want %q", tc.engine, tc.glob, got, tc.want)
			}
		})
	}
}

// TestSelect_EmptyAndJQEngine_BehaveIdentically verifies that omitting engine
// and setting it to "jq" explicitly produce extractors with identical
// behavior — the default must be unchanged from today.
func TestSelect_EmptyAndJQEngine_BehaveIdentically(t *testing.T) {
	t.Parallel()

	content := []byte("version: 1.2.3\n")

	for _, engine := range []string{"", "jq"} {
		sel, err := extractor.Select(engine, "Chart.yaml", ".version")
		if err != nil {
			t.Fatalf("Select(%q, ...): unexpected error: %v", engine, err)
		}

		got, err := sel.Extract(content)
		if err != nil {
			t.Fatalf("Extract: unexpected error: %v", err)
		}
		if !got.Present || got.Value != "1.2.3" {
			t.Errorf("Select(%q, ...).Extract() = %+v, want Present=true Value=1.2.3", engine, got)
		}
	}
}

// TestSelect_ReportedEngineMatchesTheCompilerThatRan pins the property the
// Selection type exists for: the engine a caller reads off the result is the
// engine that actually compiled the expression, not a value resolved
// independently. The expression here is valid HCL traversal syntax and not a
// valid jq program, so a Selection claiming "hcl" while running jq (or the
// reverse) could not have extracted this content successfully.
func TestSelect_ReportedEngineMatchesTheCompilerThatRan(t *testing.T) {
	t.Parallel()

	const tf = `terraform {
  required_version = ">= 1.5.0"
}
`

	sel, err := extractor.Select("", "versions.tf", "terraform.required_version")
	if err != nil {
		t.Fatalf("Select: unexpected error: %v", err)
	}
	if got := sel.Engine(); got != "hcl" {
		t.Fatalf("Engine() = %q, want hcl", got)
	}

	got, err := sel.Extract([]byte(tf))
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if !got.Present || got.Value != ">= 1.5.0" {
		t.Errorf("Extract() = %+v, want Present=true Value=\">= 1.5.0\" — the reported engine is not the one that ran", got)
	}
}

// TestSelect_UnrecognizedEngine_ReturnsError verifies Select rejects an
// unrecognized engine value rather than silently falling back to jq — and
// does so regardless of what the glob would have inferred, since an explicit
// bad value is a typo to surface, not a hint to override.
func TestSelect_UnrecognizedEngine_ReturnsError(t *testing.T) {
	t.Parallel()

	for _, glob := range []string{"Chart.yaml", "versions.tf"} {
		if _, err := extractor.Select("cobol", glob, ".version"); err == nil {
			t.Errorf("Select(\"cobol\", %q, ...) = nil error, want rejection", glob)
		}
	}
}

// TestSelect_InvalidExpr_ReturnsError verifies a malformed expression is
// rejected by whichever engine was resolved, so a bad config fails at load
// rather than at poll time.
func TestSelect_InvalidExpr_ReturnsError(t *testing.T) {
	t.Parallel()

	if _, err := extractor.Select("jq", "Chart.yaml", "{{{"); err == nil {
		t.Error("Select(jq, ..., \"{{{\") = nil error, want a compile failure")
	}
	if _, err := extractor.Select("hcl", "versions.tf", ""); err == nil {
		t.Error("Select(hcl, ..., \"\") = nil error, want a compile failure")
	}
}

// TestSelect_ReturnsFieldExtractorInterface is a compile-time-flavored check:
// a Selection must be usable wherever a FieldExtractor is expected, so the
// engine travelling with the extractor never costs a caller the ability to
// treat it as a plain extractor.
func TestSelect_ReturnsFieldExtractorInterface(t *testing.T) {
	t.Parallel()

	sel, err := extractor.Select("jq", "Chart.yaml", ".version")
	if err != nil {
		t.Fatalf("Select: unexpected error: %v", err)
	}
	var fe extractor.FieldExtractor = sel
	if fe == nil {
		t.Fatal("Select returned a nil FieldExtractor")
	}
}

func contains(s, substr string) bool {
	return len(substr) == 0 || indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
