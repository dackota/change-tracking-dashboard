package versiondelta_test

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"github.com/dackota/change-tracking-dashboard/internal/versiondelta"
)

// TestCompare_PatchBump proves the tracer path: a fix-level semver increase
// is reported as Patch, comparable.
func TestCompare_PatchBump(t *testing.T) {
	t.Parallel()

	delta, ok := versiondelta.Compare("10.1.2", "10.1.3")
	if !ok {
		t.Fatalf("Compare(10.1.2, 10.1.3) ok = false, want true")
	}
	if delta != versiondelta.Patch {
		t.Errorf("Compare(10.1.2, 10.1.3) = %v, want Patch", delta)
	}
}

// TestCompare_Table is the dense table-driven suite covering every case the
// PRD calls out: major/minor/patch bumps, downgrades on every component,
// equal values, v-prefixed versions, prerelease/build metadata, bare
// integers, non-semver constraints, floating tags, and empty strings.
func TestCompare_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		old, new_ string
		wantDelta versiondelta.Delta
		wantOK    bool
	}{
		{name: "major bump", old: "1.9.0", new_: "2.0.0", wantDelta: versiondelta.Major, wantOK: true},
		{name: "major bump across double digits", old: "9.9.9", new_: "10.0.0", wantDelta: versiondelta.Major, wantOK: true},
		{name: "minor bump", old: "1.20.3", new_: "1.21.0", wantDelta: versiondelta.Minor, wantOK: true},
		{name: "patch bump", old: "10.1.2", new_: "10.1.3", wantDelta: versiondelta.Patch, wantOK: true},
		{name: "zero-major minor bump is still minor, not major", old: "0.1.0", new_: "0.2.0", wantDelta: versiondelta.Minor, wantOK: true},

		{name: "downgrade on major component", old: "2.0.0", new_: "1.9.0", wantDelta: versiondelta.Downgrade, wantOK: true},
		{name: "downgrade on minor component", old: "1.9.0", new_: "1.8.0", wantDelta: versiondelta.Downgrade, wantOK: true},
		{name: "downgrade on patch component", old: "1.9.3", new_: "1.9.1", wantDelta: versiondelta.Downgrade, wantOK: true},

		{name: "equal values are not comparable to a delta", old: "1.2.3", new_: "1.2.3", wantOK: false},

		{name: "v-prefixed major bump", old: "v1.20.3", new_: "v2.0.0", wantDelta: versiondelta.Major, wantOK: true},
		{name: "v-prefixed patch bump", old: "v1.2.3", new_: "v1.2.4", wantDelta: versiondelta.Patch, wantOK: true},
		{name: "mixed v-prefix", old: "v1.2.3", new_: "1.3.0", wantDelta: versiondelta.Minor, wantOK: true},

		{name: "prerelease to release is comparable", old: "1.0.0-rc.1", new_: "1.0.0", wantDelta: versiondelta.Patch, wantOK: true},
		{name: "build metadata is ignored for ordering", old: "1.2.3+build.1", new_: "1.2.3+build.2", wantOK: false},

		{name: "bare integer quantity is not comparable", old: "2", new_: "3", wantOK: false},
		{name: "one-sided bare integer is not comparable", old: "2.0.0", new_: "3", wantOK: false},

		{name: "non-semver constraint is not comparable", old: "~>7.0", new_: "~>8.0", wantOK: false},
		{name: "floating tags are not comparable", old: "stable", new_: "latest", wantOK: false},

		{name: "empty old value is not comparable", old: "", new_: "1.0.0", wantOK: false},
		{name: "empty new value is not comparable", old: "1.0.0", new_: "", wantOK: false},
		{name: "both empty is not comparable", old: "", new_: "", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gotDelta, gotOK := versiondelta.Compare(tc.old, tc.new_)
			if gotOK != tc.wantOK {
				t.Fatalf("Compare(%q, %q) ok = %v, want %v (delta=%v)", tc.old, tc.new_, gotOK, tc.wantOK, gotDelta)
			}
			if tc.wantOK && gotDelta != tc.wantDelta {
				t.Errorf("Compare(%q, %q) = %v, want %v", tc.old, tc.new_, gotDelta, tc.wantDelta)
			}
		})
	}
}

// adversarialStrings feeds the property test below deliberately hostile
// input: empty, whitespace, oversized, regex-looking, unicode, and
// non-version values.
var adversarialStrings = []string{
	"", " ", "1.2.3", "v1.2.3", "2", "0", "~>7.0", "latest", "stable",
	strings.Repeat("9", 500) + ".0.0", "[invalid(regex", ".*", "日本語",
	"1.2.3-rc.1+build.5", "-1.2.3", "1.2.3.4.5",
}

type comparePair struct{ old, new_ string }

// Generate implements quick.Generator, drawing both sides independently from
// adversarialStrings so the property test sweeps combinations a hand-written
// table wouldn't think to pair.
func (comparePair) Generate(rnd *rand.Rand, size int) reflect.Value {
	return reflect.ValueOf(comparePair{
		old:  adversarialStrings[rnd.Intn(len(adversarialStrings))],
		new_: adversarialStrings[rnd.Intn(len(adversarialStrings))],
	})
}

// TestCompare_NeverPanicsAndIsDeterministic_Property proves Compare is a
// total function: it never panics on adversarial input and returns the same
// result for the same input every time.
func TestCompare_NeverPanicsAndIsDeterministic_Property(t *testing.T) {
	t.Parallel()

	property := func(p comparePair) (ok bool) {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("Compare panicked: %v", r)
				ok = false
			}
		}()

		d1, ok1 := versiondelta.Compare(p.old, p.new_)
		d2, ok2 := versiondelta.Compare(p.old, p.new_)
		if d1 != d2 || ok1 != ok2 {
			t.Logf("non-deterministic: (%v,%v) != (%v,%v)", d1, ok1, d2, ok2)
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 500}); err != nil {
		t.Error(err)
	}
}
