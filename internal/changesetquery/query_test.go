package changesetquery_test

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/changesetquery"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/filter"
	"github.com/dackota/change-tracking-dashboard/internal/store"
)

// Every test here drives the query policy directly, with a fake Source. The
// same assertions previously needed an httptest round trip against a real
// SQLite file, which is why several of these rules — the AND composition of
// impact and risk, the single-read of the risk rules — had no direct test at
// all.

// fakeSource is a changesetquery.Source test double. It records the arguments
// QueryChangesets was called with, so tests can assert on the clamped limit,
// the composed predicate, and the window that reached the store.
type fakeSource struct {
	facets    map[string][]string
	facetsErr error

	page store.ChangesetPage
	err  error

	calls     int
	gotWindow store.TimeWindow
	gotSpec   filter.FilterSpec
	gotPred   store.ChangesetPredicate
	gotCursor string
	gotLimit  int
}

func (f *fakeSource) FacetOptions() (map[string][]string, error) {
	if f.facetsErr != nil {
		return nil, f.facetsErr
	}
	if f.facets == nil {
		return map[string][]string{}, nil
	}
	return f.facets, nil
}

func (f *fakeSource) QueryChangesets(w store.TimeWindow, spec filter.FilterSpec, pred store.ChangesetPredicate, cursor string, limit int) (store.ChangesetPage, error) {
	f.calls++
	f.gotWindow, f.gotSpec, f.gotPred, f.gotCursor, f.gotLimit = w, spec, pred, cursor, limit
	return f.page, f.err
}

func run(t *testing.T, src *fakeSource, rawQuery string, rules func() []changeset.RiskRule) (changesetquery.Page, error) {
	t.Helper()
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		t.Fatalf("ParseQuery(%q): %v", rawQuery, err)
	}
	return changesetquery.New(src, rules).Run(context.Background(), values)
}

// TestMaxPageSize_WithinStoreCeiling is the drift guard between the two page
// caps. MaxPageSize is the API's page size; store.MaxChangesetPageSize is the
// store's absolute ceiling on one fetch. If the API cap ever exceeded the
// store's, the endpoint would be requesting pages the store silently refuses
// to fill, and "how big is a page" would have two contradictory answers.
func TestMaxPageSize_WithinStoreCeiling(t *testing.T) {
	t.Parallel()

	if changesetquery.MaxPageSize > store.MaxChangesetPageSize {
		t.Errorf("MaxPageSize = %d exceeds store.MaxChangesetPageSize = %d",
			changesetquery.MaxPageSize, store.MaxChangesetPageSize)
	}
	if changesetquery.DefaultPageSize > changesetquery.MaxPageSize {
		t.Errorf("DefaultPageSize = %d exceeds MaxPageSize = %d",
			changesetquery.DefaultPageSize, changesetquery.MaxPageSize)
	}
}

// TestRun_LimitPolicy covers the whole limit contract in one place: default
// when absent, clamped when oversized, rejected when meaningless.
func TestRun_LimitPolicy(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		query     string
		wantLimit int
		wantErr   bool
	}{
		{"absent uses default", "", changesetquery.DefaultPageSize, false},
		{"explicit is honored", "limit=7", 7, false},
		{"at the cap", "limit=100", 100, false},
		{"over the cap is clamped", "limit=100000", changesetquery.MaxPageSize, false},
		{"zero is rejected", "limit=0", 0, true},
		{"negative is rejected", "limit=-1", 0, true},
		{"non-numeric is rejected", "limit=lots", 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeSource{}
			_, err := run(t, src, tc.query, nil)

			if tc.wantErr {
				if !errors.Is(err, changesetquery.ErrBadRequest) {
					t.Fatalf("err = %v, want ErrBadRequest", err)
				}
				if src.calls != 0 {
					t.Error("the store was queried despite a malformed limit")
				}
				return
			}
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if src.gotLimit != tc.wantLimit {
				t.Errorf("limit reaching the store = %d, want %d", src.gotLimit, tc.wantLimit)
			}
		})
	}
}

// TestRun_ComposesImpactAndRiskWithAND is the correctness trap allPredicates'
// own doc warns about: returning the last non-nil predicate instead of ANDing
// passes every single-filter test and silently drops a filter whenever two
// are combined. Reaching this previously required an HTTP round trip.
func TestRun_ComposesImpactAndRiskWithAND(t *testing.T) {
	t.Parallel()

	// A rule that classifies any change to the "secret" field as a security
	// risk, so risk classification is controlled rather than incidental.
	rules := func() []changeset.RiskRule {
		return []changeset.RiskRule{{Name: "sec", Risk: changeset.RiskSecurity, FieldPattern: "secret"}}
	}

	// majorNoRisk: impact "major", no risk class. Passes impact, fails risk.
	majorNoRisk := assembleOne(t, "sha-major", "version", "1.0.0", "2.0.0")
	// riskNotMajor: a security risk whose version delta is only a patch.
	// Passes risk, fails impact. This is the case that distinguishes AND from
	// "whichever predicate happens to be last" — without it, both
	// implementations agree and the trap goes undetected.
	riskNotMajor := assembleOne(t, "sha-risk", "secret", "1.0.0", "1.0.1")

	src := &fakeSource{}
	if _, err := run(t, src, "impact=major&risk=security", rules); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if src.gotPred == nil {
		t.Fatal("no predicate reached the store despite both filters being set")
	}
	if src.gotPred(majorNoRisk) {
		t.Error("predicate accepted a changeset matching only the impact filter — impact AND risk, not impact alone")
	}
	if src.gotPred(riskNotMajor) {
		t.Error("predicate accepted a changeset matching only the risk filter — the filters must compose with AND, not with whichever runs last")
	}

	// Each changeset passes its own filter alone, proving the rejections above
	// came from the AND rather than from a predicate that matches nothing.
	single := &fakeSource{}
	if _, err := run(t, single, "impact=major", rules); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !single.gotPred(majorNoRisk) {
		t.Error("impact-only predicate rejected a major changeset")
	}

	riskOnly := &fakeSource{}
	if _, err := run(t, riskOnly, "risk=security", rules); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !riskOnly.gotPred(riskNotMajor) {
		t.Error("risk-only predicate rejected a security changeset")
	}
}

// TestRun_NoFilters_PassesNilPredicate proves the store's "no filtering" fast
// path is preserved: an unfiltered request must not pay for a per-changeset
// classification that always returns true.
func TestRun_NoFilters_PassesNilPredicate(t *testing.T) {
	t.Parallel()

	src := &fakeSource{}
	if _, err := run(t, src, "", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if src.gotPred != nil {
		t.Error("a predicate reached the store for an unfiltered request — nil is the store's no-filtering signal")
	}
}

// TestRun_ReadsRiskRulesExactlyOnce is the hot-reload invariant. The rules
// source is a live watcher; if Run read it twice, a reload landing between
// the filter and the response could produce badges contradicting the filter
// that selected the changesets. Page.Rules must be the same snapshot the
// predicate closed over.
func TestRun_ReadsRiskRulesExactlyOnce(t *testing.T) {
	t.Parallel()

	reads := 0
	first := []changeset.RiskRule{{Name: "first", Risk: changeset.RiskSecurity, FieldPattern: "x"}}
	second := []changeset.RiskRule{{Name: "second", Risk: changeset.RiskSecurity, FieldPattern: "y"}}
	rules := func() []changeset.RiskRule {
		reads++
		if reads == 1 {
			return first
		}
		return second // a reload landing mid-query
	}

	src := &fakeSource{}
	page, err := run(t, src, "risk=security", rules)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if reads != 1 {
		t.Errorf("rules source read %d times, want exactly 1 — a second read lets a hot reload split the response", reads)
	}
	if len(page.Rules) != 1 || page.Rules[0].Name != "first" {
		t.Errorf("Page.Rules = %+v, want the snapshot the predicate used", page.Rules)
	}
}

// TestRun_NilRulesSupplier_UsesDefaults keeps zero-config callers working.
func TestRun_NilRulesSupplier_UsesDefaults(t *testing.T) {
	t.Parallel()

	page, err := run(t, &fakeSource{}, "", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(page.Rules) != len(changeset.DefaultRiskRules()) {
		t.Errorf("Page.Rules has %d rules, want the %d built-in defaults", len(page.Rules), len(changeset.DefaultRiskRules()))
	}
}

// TestRun_ClassifiesRequestErrors pins which failures are the caller's fault.
// Everything wrapping ErrBadRequest becomes a 400; everything else a 500.
func TestRun_ClassifiesRequestErrors(t *testing.T) {
	t.Parallel()

	badRequests := []string{
		"asOf=not-a-time",
		"since=not-a-time",
		"impact=not-a-tier",
		"risk=not-a-risk",
		"limit=0",
	}
	for _, q := range badRequests {
		t.Run(q, func(t *testing.T) {
			if _, err := run(t, &fakeSource{}, q, nil); !errors.Is(err, changesetquery.ErrBadRequest) {
				t.Errorf("err = %v, want ErrBadRequest", err)
			}
		})
	}

	t.Run("invalid cursor is a bad request, not a store failure", func(t *testing.T) {
		src := &fakeSource{err: store.ErrInvalidCursor}
		if _, err := run(t, src, "cursor=garbage", nil); !errors.Is(err, changesetquery.ErrBadRequest) {
			t.Errorf("err = %v, want ErrBadRequest", err)
		}
	})

	t.Run("a store failure is not a bad request", func(t *testing.T) {
		src := &fakeSource{err: errors.New("disk on fire")}
		_, err := run(t, src, "", nil)
		if err == nil {
			t.Fatal("want an error")
		}
		if errors.Is(err, changesetquery.ErrBadRequest) {
			t.Error("a store failure was classified as a bad request — it would surface as a 400")
		}
	})

	t.Run("a facet-options failure is not a bad request", func(t *testing.T) {
		src := &fakeSource{facetsErr: errors.New("disk on fire")}
		_, err := run(t, src, "", nil)
		if err == nil {
			t.Fatal("want an error")
		}
		if errors.Is(err, changesetquery.ErrBadRequest) {
			t.Error("a facet-options failure was classified as a bad request")
		}
	})
}

// TestRun_ReservedParamsNeverBecomeFacetFilters proves the shadowing guard:
// an operator-configured facet sharing a reserved param's name must not let
// that param be reinterpreted as a facet filter.
func TestRun_ReservedParamsNeverBecomeFacetFilters(t *testing.T) {
	t.Parallel()

	// The store reports a facet literally named "repo" — the shadowing case.
	src := &fakeSource{facets: map[string][]string{
		"repo":   {"hijacked"},
		"region": {"us-west-2"},
	}}

	if _, err := run(t, src, "repo=apps-repo&region=us-west-2", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// "repo" must have been applied as the repo scope, not as a facet.
	if got := src.gotSpec.Includes()["repo"]; len(got) != 0 {
		t.Errorf(`"repo" reached the store as a facet filter with values %v — it is reserved`, got)
	}
	if got := src.gotSpec.Includes()["region"]; len(got) != 1 || got[0] != "us-west-2" {
		t.Errorf(`non-reserved facet "region" = %v, want it to survive as a filter`, got)
	}
}

// TestRun_UnknownParamsAreIgnored keeps a typo or an unrelated query param
// from becoming a 400 — matching the HTML feed handler's whitelist
// convention.
func TestRun_UnknownParamsAreIgnored(t *testing.T) {
	t.Parallel()

	src := &fakeSource{facets: map[string][]string{"region": {"us-west-2"}}}
	if _, err := run(t, src, "totallyUnknown=whatever", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if src.calls != 1 {
		t.Error("an unknown param prevented the query")
	}
}

// TestRun_TimeWindow covers the window contract: asOf defaults to now, since
// defaults to no lower bound, and an empty window is a normal answer rather
// than a caller error.
func TestRun_TimeWindow(t *testing.T) {
	t.Parallel()

	t.Run("defaults", func(t *testing.T) {
		src := &fakeSource{}
		before := time.Now()
		if _, err := run(t, src, "", nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if src.gotWindow.AsOf.Before(before) {
			t.Error("asOf defaulted to something earlier than now")
		}
		if !src.gotWindow.Since.IsZero() {
			t.Errorf("since = %v, want the zero Time (no lower bound)", src.gotWindow.Since)
		}
	})

	t.Run("since at or after asOf is an empty window, not an error", func(t *testing.T) {
		src := &fakeSource{}
		_, err := run(t, src, "since=2026-01-02T00:00:00Z&asOf=2026-01-01T00:00:00Z", nil)
		if err != nil {
			t.Fatalf("Run: %v — an inverted window is a normal empty result for a polling loop", err)
		}
		if src.calls != 1 {
			t.Error("the store was not queried for an inverted window")
		}
	})
}

// TestRun_PropagatesPageMetadata proves the cursor and examined count survive
// to the caller — NextCursor is the end-of-results signal, and Examined is
// what makes a pathological filter diagnosable.
func TestRun_PropagatesPageMetadata(t *testing.T) {
	t.Parallel()

	src := &fakeSource{page: store.ChangesetPage{
		Changesets: []changeset.Changeset{{Repo: "r", CommitSha: "sha"}},
		NextCursor: "next-page",
		Examined:   5000,
	}}

	page, err := run(t, src, "", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if page.NextCursor != "next-page" {
		t.Errorf("NextCursor = %q, want it propagated", page.NextCursor)
	}
	if page.Examined != 5000 {
		t.Errorf("Examined = %d, want it propagated", page.Examined)
	}
	if len(page.Changesets) != 1 {
		t.Errorf("got %d changesets, want 1", len(page.Changesets))
	}
}

// assembleOne builds a single-Change Changeset with the given field and
// old/new values, so a test can control both its impact tier and whether a
// risk rule matches it.
func assembleOne(t *testing.T, sha, field, oldVal, newVal string) changeset.Changeset {
	t.Helper()
	sets := changeset.Assemble([]domain.Change{{
		Repo: "r", FilePath: "tenant/Chart.yaml", Field: field,
		ChangeType: domain.ChangeTypeModified, OldValue: &oldVal, NewValue: &newVal,
		CommitSha: sha, CommittedAt: time.Now(),
	}})
	if len(sets) != 1 {
		t.Fatalf("Assemble produced %d changesets, want 1", len(sets))
	}
	return sets[0]
}
