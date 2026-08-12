package changeset_test

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
)

// TestClassifyImpact_PatchBump proves the tracer path: a changeset whose
// only change is a routine patch bump classifies as ImpactPatch — the case
// that renders blank in the Risk column today.
func TestClassifyImpact_PatchBump(t *testing.T) {
	t.Parallel()

	cs := newChangesetFixture(domain.Change{
		FilePath:   "workloads/app/values.yaml",
		Field:      "imageTags",
		ChangeType: domain.ChangeTypeModified,
		OldValue:   ptr("10.1.2"),
		NewValue:   ptr("10.1.3"),
	})

	got := changeset.ClassifyImpact(cs)

	if got != changeset.ImpactPatch {
		t.Errorf("ClassifyImpact() = %v, want %v", got, changeset.ImpactPatch)
	}
}

// TestClassifyChangeImpact_Table covers the per-change classifier's
// boundaries: forward bumps of every magnitude, and every shape of "not a
// comparable forward bump" (adds, removes, non-semver, bare integers, equal
// values, backwards moves).
func TestClassifyChangeImpact_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change domain.Change
		want   changeset.Impact
	}{
		{
			name:   "major bump",
			change: domain.Change{ChangeType: domain.ChangeTypeModified, OldValue: ptr("1.9.0"), NewValue: ptr("2.0.0")},
			want:   changeset.ImpactMajor,
		},
		{
			name:   "minor bump",
			change: domain.Change{ChangeType: domain.ChangeTypeModified, OldValue: ptr("1.20.3"), NewValue: ptr("1.21.0")},
			want:   changeset.ImpactMinor,
		},
		{
			name:   "patch bump",
			change: domain.Change{ChangeType: domain.ChangeTypeModified, OldValue: ptr("10.1.2"), NewValue: ptr("10.1.3")},
			want:   changeset.ImpactPatch,
		},
		{
			name:   "v-prefixed major bump",
			change: domain.Change{ChangeType: domain.ChangeTypeModified, OldValue: ptr("v1.20.3"), NewValue: ptr("v2.0.0")},
			want:   changeset.ImpactMajor,
		},
		{
			name:   "downgrade on major component",
			change: domain.Change{ChangeType: domain.ChangeTypeModified, OldValue: ptr("2.0.0"), NewValue: ptr("1.0.0")},
			want:   changeset.ImpactDowngrade,
		},
		{
			name:   "downgrade on minor component",
			change: domain.Change{ChangeType: domain.ChangeTypeModified, OldValue: ptr("1.9.0"), NewValue: ptr("1.8.0")},
			want:   changeset.ImpactDowngrade,
		},
		{
			name:   "downgrade on patch component",
			change: domain.Change{ChangeType: domain.ChangeTypeModified, OldValue: ptr("1.9.3"), NewValue: ptr("1.9.1")},
			want:   changeset.ImpactDowngrade,
		},
		{
			name:   "non-comparable pair numerically decreasing is other, not downgrade",
			change: domain.Change{ChangeType: domain.ChangeTypeModified, OldValue: ptr("3"), NewValue: ptr("2")},
			want:   changeset.ImpactOther,
		},
		{
			name:   "equal values are other",
			change: domain.Change{ChangeType: domain.ChangeTypeModified, OldValue: ptr("1.2.3"), NewValue: ptr("1.2.3")},
			want:   changeset.ImpactOther,
		},
		{
			name:   "added (no old value) is other",
			change: domain.Change{ChangeType: domain.ChangeTypeAdded, NewValue: ptr("2.0.0")},
			want:   changeset.ImpactOther,
		},
		{
			name:   "removed (no new value) is other",
			change: domain.Change{ChangeType: domain.ChangeTypeRemoved, OldValue: ptr("1.0.0")},
			want:   changeset.ImpactOther,
		},
		{
			name:   "non-semver constraint is other",
			change: domain.Change{ChangeType: domain.ChangeTypeModified, OldValue: ptr("~>7.0"), NewValue: ptr("~>8.0")},
			want:   changeset.ImpactOther,
		},
		{
			name:   "floating tag is other",
			change: domain.Change{ChangeType: domain.ChangeTypeModified, OldValue: ptr("stable"), NewValue: ptr("latest")},
			want:   changeset.ImpactOther,
		},
		{
			name:   "bare integer quantity is other, not a version bump",
			change: domain.Change{ChangeType: domain.ChangeTypeModified, OldValue: ptr("2"), NewValue: ptr("3")},
			want:   changeset.ImpactOther,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.change.Field = "imageTags"
			tc.change.FilePath = "workloads/app/values.yaml"
			cs := newChangesetFixture(tc.change)
			got := changeset.ClassifyChangeImpact(cs.Changes[0])
			if got != tc.want {
				t.Errorf("ClassifyChangeImpact() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestClassifyImpact_Rollup proves the changeset rollup: the tier reported
// is the highest-precedence tier among the changeset's changes, across every
// pairing, with precedence major > minor > patch > other.
func TestClassifyImpact_Rollup(t *testing.T) {
	t.Parallel()

	majorChange := domain.Change{Field: "a", ChangeType: domain.ChangeTypeModified, OldValue: ptr("1.0.0"), NewValue: ptr("2.0.0")}
	minorChange := domain.Change{Field: "b", ChangeType: domain.ChangeTypeModified, OldValue: ptr("1.0.0"), NewValue: ptr("1.1.0")}
	patchChange := domain.Change{Field: "c", ChangeType: domain.ChangeTypeModified, OldValue: ptr("1.0.0"), NewValue: ptr("1.0.1")}
	otherChange := domain.Change{Field: "d", ChangeType: domain.ChangeTypeModified, OldValue: ptr("stable"), NewValue: ptr("latest")}
	downgradeChange := domain.Change{Field: "e", ChangeType: domain.ChangeTypeModified, OldValue: ptr("2.0.0"), NewValue: ptr("1.0.0")}

	tests := []struct {
		name    string
		changes []domain.Change
		want    changeset.Impact
	}{
		{name: "single major", changes: []domain.Change{majorChange}, want: changeset.ImpactMajor},
		{name: "single minor", changes: []domain.Change{minorChange}, want: changeset.ImpactMinor},
		{name: "single patch", changes: []domain.Change{patchChange}, want: changeset.ImpactPatch},
		{name: "single other", changes: []domain.Change{otherChange}, want: changeset.ImpactOther},
		{name: "single downgrade", changes: []domain.Change{downgradeChange}, want: changeset.ImpactDowngrade},
		{name: "major beats minor", changes: []domain.Change{minorChange, majorChange}, want: changeset.ImpactMajor},
		{name: "major beats patch", changes: []domain.Change{patchChange, majorChange}, want: changeset.ImpactMajor},
		{name: "major beats other", changes: []domain.Change{otherChange, majorChange}, want: changeset.ImpactMajor},
		{name: "minor beats patch", changes: []domain.Change{patchChange, minorChange}, want: changeset.ImpactMinor},
		{name: "minor beats other", changes: []domain.Change{otherChange, minorChange}, want: changeset.ImpactMinor},
		{name: "patch beats other", changes: []domain.Change{otherChange, patchChange}, want: changeset.ImpactPatch},
		{name: "all four present, major wins", changes: []domain.Change{otherChange, patchChange, minorChange, majorChange}, want: changeset.ImpactMajor},

		// downgrade precedence: major > downgrade > minor > patch > other
		{name: "major beats downgrade", changes: []domain.Change{downgradeChange, majorChange}, want: changeset.ImpactMajor},
		{name: "downgrade beats minor", changes: []domain.Change{minorChange, downgradeChange}, want: changeset.ImpactDowngrade},
		{name: "downgrade beats patch", changes: []domain.Change{patchChange, downgradeChange}, want: changeset.ImpactDowngrade},
		{name: "downgrade beats other", changes: []domain.Change{otherChange, downgradeChange}, want: changeset.ImpactDowngrade},
		{name: "all five present, major still wins", changes: []domain.Change{otherChange, patchChange, minorChange, downgradeChange, majorChange}, want: changeset.ImpactMajor},
		{name: "downgrade wins over minor/patch/other together", changes: []domain.Change{otherChange, patchChange, minorChange, downgradeChange}, want: changeset.ImpactDowngrade},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cs := newMultiChangeFixture(tc.changes...)
			got := changeset.ClassifyImpact(cs)
			if got != tc.want {
				t.Errorf("ClassifyImpact() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestClassifyImpact_EmptyChangeset proves a changeset with no changes at
// all still yields ImpactOther rather than panicking or returning empty.
func TestClassifyImpact_EmptyChangeset(t *testing.T) {
	t.Parallel()

	cs := changeset.Changeset{Repo: "infra-repo", CommitSha: "empty-sha"}

	got := changeset.ClassifyImpact(cs)

	if got != changeset.ImpactOther {
		t.Errorf("ClassifyImpact(empty) = %v, want %v", got, changeset.ImpactOther)
	}
}

// newMultiChangeFixture builds a multi-Change Changeset for rollup tests,
// mirroring newChangesetFixture's single-Change construction.
func newMultiChangeFixture(changes ...domain.Change) changeset.Changeset {
	full := make([]domain.Change, 0, len(changes))
	for _, c := range changes {
		c.Repo = "infra-repo"
		c.CommitSha = "fixture-sha"
		c.Author = "alice"
		c.CommittedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
		full = append(full, c)
	}
	sets := changeset.Assemble(full)
	if len(sets) != 1 {
		panic("newMultiChangeFixture: Assemble did not produce exactly one Changeset")
	}
	return sets[0]
}

// impactClassifierInput is a generated, potentially-adversarial Changeset
// for the property test below.
type impactClassifierInput struct {
	cs changeset.Changeset
}

var impactAdversarialStrings = []string{
	"", " ", "1.2.3", "v1.2.3", "2", "~>7.0", "latest", "stable",
	strings.Repeat("9", 200) + ".0.0", "[invalid(regex", "日本語", "1.2.3-rc.1",
}

// Generate implements quick.Generator, building a Changeset with 0-5
// adversarial Changes, including nil old/new values in every combination.
func (impactClassifierInput) Generate(rnd *rand.Rand, size int) reflect.Value {
	n := rnd.Intn(6)
	changes := make([]changeset.Change, 0, n)
	for range n {
		var old, newv *string
		if rnd.Intn(2) == 0 {
			s := impactAdversarialStrings[rnd.Intn(len(impactAdversarialStrings))]
			old = &s
		}
		if rnd.Intn(2) == 0 {
			s := impactAdversarialStrings[rnd.Intn(len(impactAdversarialStrings))]
			newv = &s
		}
		changes = append(changes, changeset.Change{
			Change: domain.Change{
				Repo:        "fuzz-repo",
				FilePath:    impactAdversarialStrings[rnd.Intn(len(impactAdversarialStrings))],
				Field:       impactAdversarialStrings[rnd.Intn(len(impactAdversarialStrings))],
				ChangeType:  domain.ChangeTypeModified,
				OldValue:    old,
				NewValue:    newv,
				CommitSha:   "fuzz-sha",
				Author:      "fuzz-author",
				CommittedAt: time.Now(),
			},
		})
	}
	cs := changeset.Changeset{
		Repo:        "fuzz-repo",
		CommitSha:   "fuzz-sha",
		Author:      "fuzz-author",
		CommittedAt: time.Now(),
		Changes:     changes,
	}
	return reflect.ValueOf(impactClassifierInput{cs: cs})
}

// TestClassifyImpact_NeverPanicsAndIsDeterministic_Property proves both
// ClassifyChangeImpact and ClassifyImpact are total, deterministic functions
// over adversarial input, including a Changeset with no Changes at all.
func TestClassifyImpact_NeverPanicsAndIsDeterministic_Property(t *testing.T) {
	t.Parallel()

	property := func(in impactClassifierInput) (ok bool) {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("ClassifyImpact panicked: %v", r)
				ok = false
			}
		}()

		got1 := changeset.ClassifyImpact(in.cs)
		got2 := changeset.ClassifyImpact(in.cs)
		if got1 != got2 {
			t.Logf("non-deterministic: %v != %v", got1, got2)
			return false
		}
		if got1 == "" {
			t.Logf("ClassifyImpact returned empty tier")
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 300}); err != nil {
		t.Error(err)
	}
}
