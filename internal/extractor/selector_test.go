package extractor_test

import (
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/extractor"
)

// TestValidateEngine covers the legal-value contract: unset/empty and "jq" are
// the only values accepted right now. "hcl" is reserved for a future task and
// must NOT validate successfully yet — accepting it prematurely would let a
// tracker silently no-op once the hcl engine ships.
func TestValidateEngine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		engine  string
		wantErr bool
	}{
		{name: "empty defaults to jq", engine: "", wantErr: false},
		{name: "explicit jq", engine: "jq", wantErr: false},
		{name: "hcl is reserved, not yet accepted", engine: "hcl", wantErr: true},
		{name: "unknown value", engine: "bogus", wantErr: true},
		{name: "case mismatch is rejected", engine: "JQ", wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := extractor.ValidateEngine(tc.engine)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateEngine(%q) = nil, want an error", tc.engine)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateEngine(%q) = %v, want nil", tc.engine, err)
			}
		})
	}
}

// TestValidateEngine_ErrorNamesTheBadValue verifies the error is actionable —
// it must name the invalid value so a human can find the typo in their config.
func TestValidateEngine_ErrorNamesTheBadValue(t *testing.T) {
	t.Parallel()

	err := extractor.ValidateEngine("hcl")
	if err == nil {
		t.Fatal("ValidateEngine(\"hcl\") = nil, want an error")
	}
	if !contains(err.Error(), "hcl") {
		t.Errorf("error %q does not name the invalid value %q", err.Error(), "hcl")
	}
}

// TestSelect_EmptyAndJQEngine_BehaveIdentically verifies that omitting engine
// and setting it to "jq" explicitly produce extractors with identical
// behavior — the default must be unchanged from today.
func TestSelect_EmptyAndJQEngine_BehaveIdentically(t *testing.T) {
	t.Parallel()

	content := []byte("version: 1.2.3\n")

	for _, engine := range []string{"", "jq"} {
		fe, err := extractor.Select(engine, ".version")
		if err != nil {
			t.Fatalf("Select(%q, .version): unexpected error: %v", engine, err)
		}

		got, err := fe.Extract(content)
		if err != nil {
			t.Fatalf("Extract: unexpected error: %v", err)
		}
		if !got.Present || got.Value != "1.2.3" {
			t.Errorf("Select(%q, ...).Extract() = %+v, want Present=true Value=1.2.3", engine, got)
		}
	}
}

// TestSelect_UnrecognizedEngine_ReturnsError verifies Select rejects an
// unrecognized engine value rather than silently falling back to jq.
func TestSelect_UnrecognizedEngine_ReturnsError(t *testing.T) {
	t.Parallel()

	_, err := extractor.Select("hcl", ".version")
	if err == nil {
		t.Fatal("Select(\"hcl\", ...) = nil error, want rejection")
	}
}

// TestSelect_ReturnsFieldExtractorInterface is a compile-time-flavored check:
// Select's return type must be usable wherever a FieldExtractor is expected,
// proving *Extractor satisfies the interface through the selector seam.
func TestSelect_ReturnsFieldExtractorInterface(t *testing.T) {
	t.Parallel()

	var fe extractor.FieldExtractor
	fe, err := extractor.Select("jq", ".version")
	if err != nil {
		t.Fatalf("Select: unexpected error: %v", err)
	}
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
