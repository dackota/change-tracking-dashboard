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

// newChangesetFixture builds a single-Change Changeset for table tests below.
// The Kind is derived exactly as production code derives it (via
// changeset.Assemble), so a test can drive Risk classification purely from
// FilePath/Field/Key/OldValue/NewValue/ChangeType — the fields actually
// carried on a persisted domain.Change — without hand-computing Kind.
func newChangesetFixture(c domain.Change) changeset.Changeset {
	c.Repo = "infra-repo"
	c.CommitSha = "fixture-sha"
	c.Author = "alice"
	c.CommittedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	sets := changeset.Assemble([]domain.Change{c})
	if len(sets) != 1 {
		panic("newChangesetFixture: Assemble did not produce exactly one Changeset")
	}
	return sets[0]
}

// TestClassifyRisk_ReplaceDestroy proves acceptance criterion 2: a change
// that removes a resource, or alters a replacement-forcing attribute, is
// classified replace/destroy — driven entirely by changeset.DefaultRiskRules
// (config), never a hardcoded resource-name check.
func TestClassifyRisk_ReplaceDestroy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cs   changeset.Changeset
		want []changeset.Risk
	}{
		{
			name: "removing a resource attribute is classified replace/destroy",
			cs: newChangesetFixture(domain.Change{
				FilePath:   "oci-containerengine-nodepool.tf",
				Field:      "node-pool-machine-type",
				ChangeType: domain.ChangeTypeRemoved,
				OldValue:   ptr("e2-medium"),
			}),
			want: []changeset.Risk{changeset.RiskReplaceDestroy},
		},
		{
			name: "changing a known replacement-forcing attribute is classified replace/destroy",
			cs: newChangesetFixture(domain.Change{
				FilePath:   "oci-containerengine-nodepool.tf",
				Field:      "node-pool-availability-domain",
				ChangeType: domain.ChangeTypeModified,
				OldValue:   ptr("AD-1"),
				NewValue:   ptr("AD-2"),
			}),
			want: []changeset.Risk{changeset.RiskReplaceDestroy},
		},
		{
			name: "modifying a non-replacement-forcing resource attribute is not replace/destroy",
			cs: newChangesetFixture(domain.Change{
				FilePath:   "oci-containerengine-nodepool.tf",
				Field:      "node-pool-display-name",
				ChangeType: domain.ChangeTypeModified,
				OldValue:   ptr("pool-a"),
				NewValue:   ptr("pool-b"),
			}),
			want: nil,
		},
		{
			name: "removing a non-resource (variable) field is not replace/destroy",
			cs: newChangesetFixture(domain.Change{
				FilePath:   "variables.tf",
				Field:      "gitops-repo-url",
				ChangeType: domain.ChangeTypeRemoved,
				OldValue:   ptr("https://example.com/repo.git"),
			}),
			want: nil,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := changeset.ClassifyRisk(tc.cs, changeset.DefaultRiskRules())

			assertRisksEqual(t, got, tc.want)
		})
	}
}

// TestClassifyRisk_Security proves acceptance criterion 3, using the
// dogfood security-list-widened-to-0.0.0.0/0 and opened-SSH tripwires
// (oci-security-list-worker-nodes.tf) as the fixtures.
func TestClassifyRisk_Security(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cs   changeset.Changeset
		want []changeset.Risk
	}{
		{
			name: "widening a security-list ingress source to 0.0.0.0/0 is security",
			cs: newChangesetFixture(domain.Change{
				FilePath:   "oci-security-list-worker-nodes.tf",
				Field:      "worker-nodes-ingress-source",
				ChangeType: domain.ChangeTypeModified,
				OldValue:   ptr("10.0.0.0/24"),
				NewValue:   ptr("0.0.0.0/0"),
			}),
			want: []changeset.Risk{changeset.RiskSecurity},
		},
		{
			name: "widening an egress destination to 0.0.0.0/0 is security",
			cs: newChangesetFixture(domain.Change{
				FilePath:   "oci-security-list-worker-nodes.tf",
				Field:      "worker-nodes-egress-destination",
				ChangeType: domain.ChangeTypeModified,
				OldValue:   ptr("10.0.0.0/24"),
				NewValue:   ptr("0.0.0.0/0"),
			}),
			want: []changeset.Risk{changeset.RiskSecurity},
		},
		{
			name: "opening SSH (port 22) is security",
			cs: newChangesetFixture(domain.Change{
				FilePath:   "oci-security-list-worker-nodes.tf",
				Field:      "worker-nodes-ssh-ingress-min-port",
				ChangeType: domain.ChangeTypeModified,
				OldValue:   ptr("10250"),
				NewValue:   ptr("22"),
			}),
			want: []changeset.Risk{changeset.RiskSecurity},
		},
		{
			name: "narrowing a security-list source away from 0.0.0.0/0 is not security",
			cs: newChangesetFixture(domain.Change{
				FilePath:   "oci-security-list-worker-nodes.tf",
				Field:      "worker-nodes-ingress-source",
				ChangeType: domain.ChangeTypeModified,
				OldValue:   ptr("0.0.0.0/0"),
				NewValue:   ptr("10.0.0.0/24"),
			}),
			want: nil,
		},
		{
			name: "an unrelated attribute change is not security",
			cs: newChangesetFixture(domain.Change{
				FilePath:   "oci-security-list-worker-nodes.tf",
				Field:      "worker-nodes-description",
				ChangeType: domain.ChangeTypeModified,
				OldValue:   ptr("old description"),
				NewValue:   ptr("new description"),
			}),
			want: nil,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := changeset.ClassifyRisk(tc.cs, changeset.DefaultRiskRules())

			assertRisksEqual(t, got, tc.want)
		})
	}
}

// TestClassifyRisk_CostTripwire proves acceptance criterion 4, using the
// dogfood node-pool size/OCPU/memory/boot-volume and budget-amount
// tripwires (oci-containerengine-nodepool.tf, budget.tf) as the fixtures.
func TestClassifyRisk_CostTripwire(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cs   changeset.Changeset
		want []changeset.Risk
	}{
		{
			name: "raising node-pool size is cost tripwire",
			cs: newChangesetFixture(domain.Change{
				FilePath:   "oci-containerengine-nodepool.tf",
				Field:      "node-pool-size",
				ChangeType: domain.ChangeTypeModified,
				OldValue:   ptr("2"),
				NewValue:   ptr("3"),
			}),
			want: []changeset.Risk{changeset.RiskCostTripwire},
		},
		{
			name: "raising node-pool ocpus is cost tripwire",
			cs: newChangesetFixture(domain.Change{
				FilePath:   "oci-containerengine-nodepool.tf",
				Field:      "node-pool-ocpus",
				ChangeType: domain.ChangeTypeModified,
				OldValue:   ptr("2"),
				NewValue:   ptr("4"),
			}),
			want: []changeset.Risk{changeset.RiskCostTripwire},
		},
		{
			name: "raising node-pool memory_in_gbs is cost tripwire",
			cs: newChangesetFixture(domain.Change{
				FilePath:   "oci-containerengine-nodepool.tf",
				Field:      "node-pool-memory-in-gbs",
				ChangeType: domain.ChangeTypeModified,
				OldValue:   ptr("12"),
				NewValue:   ptr("24"),
			}),
			want: []changeset.Risk{changeset.RiskCostTripwire},
		},
		{
			name: "raising boot-volume size is cost tripwire",
			cs: newChangesetFixture(domain.Change{
				FilePath:   "oci-containerengine-nodepool.tf",
				Field:      "node-pool-boot-volume-size-in-gbs",
				ChangeType: domain.ChangeTypeModified,
				OldValue:   ptr("100"),
				NewValue:   ptr("200"),
			}),
			want: []changeset.Risk{changeset.RiskCostTripwire},
		},
		{
			name: "changing the budget amount is cost tripwire",
			cs: newChangesetFixture(domain.Change{
				FilePath:   "budget.tf",
				Field:      "budget-amount",
				ChangeType: domain.ChangeTypeModified,
				OldValue:   ptr("1"),
				NewValue:   ptr("100"),
			}),
			want: []changeset.Risk{changeset.RiskCostTripwire},
		},
		{
			name: "changing an unrelated node-pool attribute is not cost tripwire",
			cs: newChangesetFixture(domain.Change{
				FilePath:   "oci-containerengine-nodepool.tf",
				Field:      "node-pool-display-name",
				ChangeType: domain.ChangeTypeModified,
				OldValue:   ptr("pool-a"),
				NewValue:   ptr("pool-b"),
			}),
			want: nil,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := changeset.ClassifyRisk(tc.cs, changeset.DefaultRiskRules())

			assertRisksEqual(t, got, tc.want)
		})
	}
}

// TestClassifyRisk_EmptySet proves the "empty set" half of acceptance
// criterion 6: a changeset that trips no rule classifies to zero risk
// classes (nil, not an error or panic) — a Change is not risky by default.
func TestClassifyRisk_EmptySet(t *testing.T) {
	t.Parallel()

	cs := newChangesetFixture(domain.Change{
		FilePath:   "oci-vcn.tf",
		Field:      "vcn-display-name",
		ChangeType: domain.ChangeTypeModified,
		OldValue:   ptr("old-name"),
		NewValue:   ptr("new-name"),
	})

	got := changeset.ClassifyRisk(cs, changeset.DefaultRiskRules())

	if len(got) != 0 {
		t.Errorf("ClassifyRisk() = %v, want empty (zero risk classes)", got)
	}
}

// TestClassifyRisk_NoRules_YieldsEmptySetNotPanic proves ClassifyRisk is
// total even over a nil/empty rule set.
func TestClassifyRisk_NoRules_YieldsEmptySetNotPanic(t *testing.T) {
	t.Parallel()

	cs := newChangesetFixture(domain.Change{
		FilePath:   "budget.tf",
		Field:      "budget-amount",
		ChangeType: domain.ChangeTypeModified,
		OldValue:   ptr("1"),
		NewValue:   ptr("100"),
	})

	got := changeset.ClassifyRisk(cs, nil)

	if len(got) != 0 {
		t.Errorf("ClassifyRisk(cs, nil) = %v, want empty", got)
	}
}

// TestClassifyRisk_NewConfigRule_YieldsNewClassificationWithNoCodeChange
// proves acceptance criterion 5's "no code change" half directly: a brand
// new RiskRule — naming a resource/attribute pattern that appears nowhere in
// changeset's source — added purely as data to the rules slice handed to
// ClassifyRisk is honored, with zero edits to ClassifyRisk or ruleMatches.
// This is the same exercise an operator does by editing config.
func TestClassifyRisk_NewConfigRule_YieldsNewClassificationWithNoCodeChange(t *testing.T) {
	t.Parallel()

	const madeUpRisk = changeset.Risk("onboarding-tripwire")
	rules := []changeset.RiskRule{
		{
			Name:            "acme-widget-quota",
			Risk:            madeUpRisk,
			FilePathPattern: `acme_widget\.tf`,
			FieldPattern:    `quota`,
		},
	}

	cs := newChangesetFixture(domain.Change{
		FilePath:   "acme_widget.tf",
		Field:      "widget-quota",
		ChangeType: domain.ChangeTypeModified,
		OldValue:   ptr("10"),
		NewValue:   ptr("1000"),
	})

	got := changeset.ClassifyRisk(cs, rules)

	assertRisksEqual(t, got, []changeset.Risk{madeUpRisk})
}

// riskClassifierInput is a generated, potentially-adversarial Changeset for
// the property test below: empty FilePath/Field, oversized values, regex
// metacharacters, unicode, and nil OldValue/NewValue in every combination —
// the input shapes a hand-picked example table would not think to cover.
type riskClassifierInput struct {
	cs changeset.Changeset
}

// adversarialStrings are drawn from for every string-valued field on a
// generated Change: empty, whitespace, an oversized string, regex
// metacharacters/anchors that could break a naively-built pattern, and
// unicode.
var adversarialStrings = []string{
	"",
	" ",
	"0.0.0.0/0",
	"22",
	strings.Repeat("x", 10000),
	`[invalid(regex`,
	`.*`,
	`$^`,
	"\x00\x01",
	"budget-amount",
	"日本語のフィールド",
	"size",
}

var adversarialKinds = []changeset.Kind{
	changeset.KindChart, changeset.KindValue,
	changeset.KindProvider, changeset.KindModule, changeset.KindResource, changeset.KindVariable,
	changeset.Kind("totally-unknown-kind"), changeset.Kind(""),
}

var adversarialChangeTypes = []domain.ChangeType{
	domain.ChangeTypeAdded, domain.ChangeTypeRemoved, domain.ChangeTypeModified,
	domain.ChangeType("unknown-change-type"), domain.ChangeType(""),
}

// Generate implements quick.Generator, building a Changeset with 0-5
// adversarial Changes so the property test sweeps structurally varied
// changesets, not just varied field values on a single Change.
func (riskClassifierInput) Generate(rnd *rand.Rand, size int) reflect.Value {
	n := rnd.Intn(6)
	changes := make([]changeset.Change, 0, n)
	for i := 0; i < n; i++ {
		var old, newv *string
		if rnd.Intn(2) == 0 {
			s := adversarialStrings[rnd.Intn(len(adversarialStrings))]
			old = &s
		}
		if rnd.Intn(2) == 0 {
			s := adversarialStrings[rnd.Intn(len(adversarialStrings))]
			newv = &s
		}
		changes = append(changes, changeset.Change{
			Change: domain.Change{
				Repo:        "fuzz-repo",
				FilePath:    adversarialStrings[rnd.Intn(len(adversarialStrings))],
				Field:       adversarialStrings[rnd.Intn(len(adversarialStrings))],
				ChangeType:  adversarialChangeTypes[rnd.Intn(len(adversarialChangeTypes))],
				OldValue:    old,
				NewValue:    newv,
				CommitSha:   "fuzz-sha",
				Author:      "fuzz-author",
				CommittedAt: time.Now(),
			},
			Kind: adversarialKinds[rnd.Intn(len(adversarialKinds))],
		})
	}
	cs := changeset.Changeset{
		Repo:        "fuzz-repo",
		CommitSha:   "fuzz-sha",
		Author:      "fuzz-author",
		CommittedAt: time.Now(),
		Changes:     changes,
	}
	return reflect.ValueOf(riskClassifierInput{cs: cs})
}

// TestClassifyRisk_NeverPanicsAndIsDeterministic_Property proves acceptance
// criterion 6: for any well-formed-or-adversarial Changeset, ClassifyRisk
// (a) never panics, (b) returns a stable/sorted set with no duplicates, and
// (c) is deterministic — calling it twice on the same input yields the same
// result. Run against both DefaultRiskRules and a rule set carrying
// deliberately-invalid regex patterns, since a malformed pattern must still
// leave the classifier total (see matchesPattern's documented fallback).
func TestClassifyRisk_NeverPanicsAndIsDeterministic_Property(t *testing.T) {
	t.Parallel()

	rulesets := map[string][]changeset.RiskRule{
		"default rules": changeset.DefaultRiskRules(),
		"rules with bad regex": {
			{Name: "bad-filepath", Risk: changeset.RiskSecurity, FilePathPattern: `[unterminated`},
			{Name: "bad-field", Risk: changeset.RiskCostTripwire, FieldPattern: `(unterminated`},
			{Name: "bad-value", Risk: changeset.RiskReplaceDestroy, ValuePattern: `*invalid`},
		},
	}

	for name, rules := range rulesets {
		rules := rules
		t.Run(name, func(t *testing.T) {
			property := func(in riskClassifierInput) (ok bool) {
				defer func() {
					if r := recover(); r != nil {
						t.Logf("ClassifyRisk panicked: %v", r)
						ok = false
					}
				}()

				got1 := changeset.ClassifyRisk(in.cs, rules)
				got2 := changeset.ClassifyRisk(in.cs, rules)

				if !reflect.DeepEqual(got1, got2) {
					t.Logf("non-deterministic: %v != %v", got1, got2)
					return false
				}
				seen := map[changeset.Risk]bool{}
				for i, r := range got1 {
					if seen[r] {
						t.Logf("duplicate risk %q in result %v", r, got1)
						return false
					}
					seen[r] = true
					if i > 0 && got1[i-1] >= r {
						t.Logf("result %v is not sorted ascending", got1)
						return false
					}
				}
				return true
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 300}); err != nil {
				t.Error(err)
			}
		})
	}
}

// assertRisksEqual compares two Risk slices order-insensitively (the
// classifier's contract is a set, not a sequence).
func assertRisksEqual(t *testing.T, got, want []changeset.Risk) {
	t.Helper()

	gotSet := map[changeset.Risk]bool{}
	for _, r := range got {
		gotSet[r] = true
	}
	wantSet := map[changeset.Risk]bool{}
	for _, r := range want {
		wantSet[r] = true
	}
	if len(gotSet) != len(wantSet) {
		t.Fatalf("ClassifyRisk() = %v, want %v", got, want)
	}
	for r := range wantSet {
		if !gotSet[r] {
			t.Fatalf("ClassifyRisk() = %v, want %v (missing %q)", got, want, r)
		}
	}
}
