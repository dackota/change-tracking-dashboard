package domain_test

import (
	"strings"
	"testing"
	"testing/quick"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
)

// TestTenantPathOf_UsesForwardSlashesOnEveryPlatform is the regression that
// motivated this type. FilePath is a git path — forward-slash separated
// everywhere — so the derived TenantPath must be too. Deriving with
// filepath.Dir instead would pass on Linux and silently produce a
// backslash-separated value on Windows, which then fails to match the
// forward-slash spelling the client sends back, rejecting a legitimate
// request indistinguishably from an unknown changeset.
func TestTenantPathOf_UsesForwardSlashesOnEveryPlatform(t *testing.T) {
	t.Parallel()

	cases := []struct {
		filePath string
		want     domain.TenantPath
	}{
		{"tenants/zero/Chart.yaml", "tenants/zero"},
		{"envs/prod/main.tf", "envs/prod"},
		{"a/b/c/d/values.yaml", "a/b/c/d"},
		{"Chart.yaml", "."},
		{"", "."},
	}

	for _, tc := range cases {
		got := domain.TenantPathOf(domain.Change{FilePath: tc.filePath})
		if got != tc.want {
			t.Errorf("TenantPathOf(%q) = %q, want %q", tc.filePath, got, tc.want)
		}
		if strings.Contains(got.String(), `\`) {
			t.Errorf("TenantPathOf(%q) = %q, contains a backslash — must stay a git path on every platform", tc.filePath, got)
		}
	}
}

// TestTenantPathOf_NeverEmitsABackslash_Property generalizes the regression
// over arbitrary input: whatever a Change's FilePath contains, the derived
// TenantPath introduces no OS-specific separator. A backslash can only appear
// in the output if one was already in the input.
func TestTenantPathOf_NeverEmitsABackslash_Property(t *testing.T) {
	t.Parallel()

	err := quick.Check(func(filePath string) bool {
		got := domain.TenantPathOf(domain.Change{FilePath: filePath}).String()
		if strings.Contains(filePath, `\`) {
			return true // a backslash in, a backslash out is not the derivation's doing
		}
		return !strings.Contains(got, `\`)
	}, nil)
	if err != nil {
		t.Error(err)
	}
}

// TestTenantPath_RoundTripsThroughTheWire proves the value rendered into
// data-tenant-path and sent back as the "path" query parameter compares equal
// to the one the authorization gate derives. This is the whole contract: the
// render side and the authorize side must agree.
func TestTenantPath_RoundTripsThroughTheWire(t *testing.T) {
	t.Parallel()

	err := quick.Check(func(filePath string) bool {
		derived := domain.TenantPathOf(domain.Change{FilePath: filePath})
		// Exactly what the browser does: read the rendered attribute, send it
		// back, and let the handler parse it.
		returned := domain.ParseTenantPath(derived.String())
		return returned == derived
	}, nil)
	if err != nil {
		t.Error(err)
	}
}

// TestParseTenantPath_DoesNotNormalize pins the deliberate choice not to
// clean the caller-supplied value. Normalizing would make spellings compare
// equal that TenantPathOf can never produce, widening the set of strings that
// pass the authorization gate rather than narrowing it. A "path" parameter
// that does not match a real Change's derived TenantPath must simply fail to
// match.
func TestParseTenantPath_DoesNotNormalize(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"envs/prod/", "./envs/prod", "envs//prod", "envs/../envs/prod"} {
		if got := domain.ParseTenantPath(raw); got.String() != raw {
			t.Errorf("ParseTenantPath(%q) = %q, want it preserved verbatim", raw, got)
		}
		if domain.ParseTenantPath(raw) == domain.TenantPathOf(domain.Change{FilePath: "envs/prod/main.tf"}) {
			t.Errorf("ParseTenantPath(%q) compares equal to a derived TenantPath — the gate would accept a spelling TenantPathOf never produces", raw)
		}
	}
}
