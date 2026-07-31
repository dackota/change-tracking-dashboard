package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/filter"
	"github.com/dackota/change-tracking-dashboard/internal/store"
)

func ptr(s string) *string { return &s }

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// queryFeed reads every persisted Change back through QueryChangesets — the
// one query path production uses — flattening the Changesets into a
// newest-commit-first slice of Changes. It exists so round-trip tests can
// assert on what was stored without a second, test-only SELECT that could
// disagree with the real one. It walks the cursor to completion, so no caller
// has to think about the store's page ceiling.
func queryFeed(t *testing.T, s *store.Store) []domain.Change {
	t.Helper()

	// An upper bound comfortably past any fixture's commit time; the window
	// is unbounded below, so this selects everything.
	w := store.TimeWindow{AsOf: time.Now().Add(24 * time.Hour)}

	var out []domain.Change
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 100 {
			t.Fatal("queryFeed: cursor walk did not terminate")
		}
		page, err := s.QueryChangesets(w, filter.FilterSpec{}, nil, cursor, store.MaxChangesetPageSize)
		if err != nil {
			t.Fatalf("QueryChangesets (cursor=%q): %v", cursor, err)
		}
		for _, cs := range page.Changesets {
			for _, c := range cs.Changes {
				out = append(out, c.Change)
			}
		}
		if page.NextCursor == "" {
			return out
		}
		cursor = page.NextCursor
	}
}

func TestPersistAndQueryChangesets(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	changes := []domain.Change{
		{
			Repo:        "apps-repo",
			FilePath:    "apps/tenant-zero/dev/us-west-2/Chart.yaml",
			Field:       "aidp-version",
			Key:         nil,
			ChangeType:  domain.ChangeTypeModified,
			OldValue:    ptr("1.0.0"),
			NewValue:    ptr("1.1.0"),
			Facets:      map[string]string{"tenant": "tenant-zero", "env": "dev", "region": "us-west-2"},
			CommitSha:   "sha-001",
			Author:      "alice",
			CommittedAt: base,
		},
		{
			Repo:        "apps-repo",
			FilePath:    "apps/tenant-one/prod/eu-west-1/Chart.yaml",
			Field:       "aidp-version",
			Key:         nil,
			ChangeType:  domain.ChangeTypeModified,
			OldValue:    ptr("2.0.0"),
			NewValue:    ptr("2.1.0"),
			Facets:      map[string]string{"tenant": "tenant-one", "env": "prod", "region": "eu-west-1"},
			CommitSha:   "sha-002",
			Author:      "bob",
			CommittedAt: base.Add(time.Hour),
		},
	}

	for _, c := range changes {
		if err := s.SaveChange(c); err != nil {
			t.Fatalf("SaveChange: %v", err)
		}
	}

	feed := queryFeed(t, s)

	if len(feed) != 2 {
		t.Fatalf("queryFeed returned %d changes, want 2", len(feed))
	}

	// Newest first: sha-002 (base+1h) should be first.
	if feed[0].CommitSha != "sha-002" {
		t.Errorf("feed[0].CommitSha = %q, want sha-002 (newest first)", feed[0].CommitSha)
	}
	if feed[1].CommitSha != "sha-001" {
		t.Errorf("feed[1].CommitSha = %q, want sha-001", feed[1].CommitSha)
	}

	// Verify round-trip of facets.
	if feed[0].Facets["tenant"] != "tenant-one" {
		t.Errorf("facet tenant: got %q, want tenant-one", feed[0].Facets["tenant"])
	}
	if feed[1].Facets["env"] != "dev" {
		t.Errorf("facet env: got %q, want dev", feed[1].Facets["env"])
	}
}

func TestHighWaterMark(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	const filePath = "apps/tenant-zero/dev/us-west-2/Chart.yaml"
	const field = "chart-version"

	// Reading before any write returns empty string.
	sha, err := s.GetHighWaterMark("apps-repo", filePath, field)
	if err != nil {
		t.Fatalf("GetHighWaterMark (empty): %v", err)
	}
	if sha.Sha != "" {
		t.Errorf("GetHighWaterMark (empty): got %q, want empty string", sha.Sha)
	}

	// Write a mark.
	if err := s.SetHighWaterMark("apps-repo", filePath, field, store.HighWaterMark{Sha: "abc123"}); err != nil {
		t.Fatalf("SetHighWaterMark: %v", err)
	}

	// Read it back.
	sha, err = s.GetHighWaterMark("apps-repo", filePath, field)
	if err != nil {
		t.Fatalf("GetHighWaterMark (after set): %v", err)
	}
	if sha.Sha != "abc123" {
		t.Errorf("GetHighWaterMark: got %q, want abc123", sha.Sha)
	}

	// Overwrite the mark.
	if err := s.SetHighWaterMark("apps-repo", filePath, field, store.HighWaterMark{Sha: "def456"}); err != nil {
		t.Fatalf("SetHighWaterMark (overwrite): %v", err)
	}
	sha, err = s.GetHighWaterMark("apps-repo", filePath, field)
	if err != nil {
		t.Fatalf("GetHighWaterMark (overwrite): %v", err)
	}
	if sha.Sha != "def456" {
		t.Errorf("GetHighWaterMark (overwrite): got %q, want def456", sha.Sha)
	}
}

// TestHighWaterMark_PerFileGranularity verifies that the HWM is keyed at
// (repo, filePath) granularity — two different files in the same repo each
// resume independently and do not clobber each other's mark. This is the
// critical correctness property that glob fan-out depends on: walking many
// matched files through the same repo must not share one cursor.
func TestHighWaterMark_PerFileGranularity(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	const repo = "apps-repo"
	const fileA = "a/x/Chart.yaml"
	const fileB = "a/y/Chart.yaml"
	const field = "chart-version"

	if err := s.SetHighWaterMark(repo, fileA, field, store.HighWaterMark{Sha: "sha-a-1"}); err != nil {
		t.Fatalf("SetHighWaterMark (fileA): %v", err)
	}
	if err := s.SetHighWaterMark(repo, fileB, field, store.HighWaterMark{Sha: "sha-b-1"}); err != nil {
		t.Fatalf("SetHighWaterMark (fileB): %v", err)
	}

	gotA, err := s.GetHighWaterMark(repo, fileA, field)
	if err != nil {
		t.Fatalf("GetHighWaterMark (fileA): %v", err)
	}
	if gotA.Sha != "sha-a-1" {
		t.Errorf("GetHighWaterMark(fileA) = %q, want sha-a-1 (must not be clobbered by fileB)", gotA.Sha)
	}

	gotB, err := s.GetHighWaterMark(repo, fileB, field)
	if err != nil {
		t.Fatalf("GetHighWaterMark (fileB): %v", err)
	}
	if gotB.Sha != "sha-b-1" {
		t.Errorf("GetHighWaterMark(fileB) = %q, want sha-b-1", gotB.Sha)
	}

	// Advancing fileA's mark must not affect fileB's.
	if err := s.SetHighWaterMark(repo, fileA, field, store.HighWaterMark{Sha: "sha-a-2"}); err != nil {
		t.Fatalf("SetHighWaterMark (fileA advance): %v", err)
	}
	gotB2, err := s.GetHighWaterMark(repo, fileB, field)
	if err != nil {
		t.Fatalf("GetHighWaterMark (fileB after fileA advance): %v", err)
	}
	if gotB2.Sha != "sha-b-1" {
		t.Errorf("GetHighWaterMark(fileB) after advancing fileA = %q, want unchanged sha-b-1", gotB2.Sha)
	}
}

// TestHighWaterMark_PerFieldGranularity verifies that the HWM is keyed by
// (repo, filePath, field): two fields tracked from the SAME file each resume
// from their own cursor and never clobber each other. This is the property the
// per-file-shared-cursor bug violated — the first field polled advanced the
// shared mark, silently starving every other field's backfill.
func TestHighWaterMark_PerFieldGranularity(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	const repo = "infra-repo"
	const file = "terraform/versions.tf"
	const fieldA = "kubernetes-version"
	const fieldB = "oci-provider-version"

	if err := s.SetHighWaterMark(repo, file, fieldA, store.HighWaterMark{Sha: "sha-a-1"}); err != nil {
		t.Fatalf("SetHighWaterMark (fieldA): %v", err)
	}
	if err := s.SetHighWaterMark(repo, file, fieldB, store.HighWaterMark{Sha: "sha-b-1"}); err != nil {
		t.Fatalf("SetHighWaterMark (fieldB): %v", err)
	}

	gotA, err := s.GetHighWaterMark(repo, file, fieldA)
	if err != nil {
		t.Fatalf("GetHighWaterMark (fieldA): %v", err)
	}
	if gotA.Sha != "sha-a-1" {
		t.Errorf("GetHighWaterMark(fieldA) = %q, want sha-a-1 (must not be clobbered by fieldB on the same file)", gotA.Sha)
	}

	gotB, err := s.GetHighWaterMark(repo, file, fieldB)
	if err != nil {
		t.Fatalf("GetHighWaterMark (fieldB): %v", err)
	}
	if gotB.Sha != "sha-b-1" {
		t.Errorf("GetHighWaterMark(fieldB) = %q, want sha-b-1", gotB.Sha)
	}

	// Advancing fieldA's mark on the shared file must not touch fieldB's.
	if err := s.SetHighWaterMark(repo, file, fieldA, store.HighWaterMark{Sha: "sha-a-2"}); err != nil {
		t.Fatalf("SetHighWaterMark (fieldA advance): %v", err)
	}
	gotB2, err := s.GetHighWaterMark(repo, file, fieldB)
	if err != nil {
		t.Fatalf("GetHighWaterMark (fieldB after fieldA advance): %v", err)
	}
	if gotB2.Sha != "sha-b-1" {
		t.Errorf("GetHighWaterMark(fieldB) after advancing fieldA on the same file = %q, want unchanged sha-b-1", gotB2.Sha)
	}
}

// TestSaveChange_Idempotent verifies that saving the same change twice — same
// (repo, file_path, field, key, commit_sha) identity — records it once. This is
// what lets a one-time cursor rebuild re-walk history without duplicating rows
// already in the feed. Covers both a scalar-key (nil) change and a keyed one,
// since SQLite's NULL-distinct rule would otherwise let nil-key rows duplicate.
func TestSaveChange_Idempotent(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	key := "argo-cd"
	scalar := domain.Change{
		Repo: "infra-repo", FilePath: "terraform/oci-containerengine-cluster.tf",
		Field: "kubernetes-version", Key: nil, ChangeType: domain.ChangeTypeModified,
		OldValue: ptr("v1.35.1"), NewValue: ptr("v1.36.1"),
		CommitSha: "sha-k8s", Author: "dev", CommittedAt: base,
	}
	keyed := domain.Change{
		Repo: "apps-repo", FilePath: "gitops/platform/argocd/Chart.yaml",
		Field: "chartDependencies", Key: &key, ChangeType: domain.ChangeTypeModified,
		OldValue: ptr("10.1.2"), NewValue: ptr("10.1.3"),
		CommitSha: "sha-argo", Author: "dev", CommittedAt: base.Add(time.Hour),
	}

	for _, c := range []domain.Change{scalar, keyed} {
		// Save each change twice — the second write must be a no-op.
		if err := s.SaveChange(c); err != nil {
			t.Fatalf("SaveChange (first): %v", err)
		}
		if err := s.SaveChange(c); err != nil {
			t.Fatalf("SaveChange (duplicate): %v", err)
		}
	}

	feed := queryFeed(t, s)
	if len(feed) != 2 {
		t.Fatalf("queryFeed returned %d changes, want 2 (each saved-twice change recorded once)", len(feed))
	}
}

func TestQueryChangesetsEmptyDatabase(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	feed := queryFeed(t, s)
	if len(feed) != 0 {
		t.Errorf("queryFeed (empty): got %d changes, want 0", len(feed))
	}
}

// TestKeyedChangeRoundTrip confirms that a Change with a non-nil Key persists
// and reads back with its Key intact through SaveChange → QueryChangesets.
func TestKeyedChangeRoundTrip(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	key := "aidp-gateway"
	c := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "aidp/k8/Chart.yaml",
		Field:       "subchart-versions",
		Key:         &key,
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("0.38.0"),
		NewValue:    ptr("0.39.0"),
		Facets:      map[string]string{"env": "dev"},
		CommitSha:   "sha-keyed-001",
		Author:      "alice",
		CommittedAt: time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC),
	}

	if err := s.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	feed := queryFeed(t, s)

	if len(feed) != 1 {
		t.Fatalf("queryFeed returned %d changes, want 1", len(feed))
	}

	got := feed[0]

	// Key must round-trip.
	if got.Key == nil {
		t.Fatal("Key is nil after round-trip, want non-nil")
	}
	if *got.Key != key {
		t.Errorf("Key = %q, want %q", *got.Key, key)
	}

	// Other fields must also be intact.
	if got.ChangeType != domain.ChangeTypeModified {
		t.Errorf("ChangeType = %q, want modified", got.ChangeType)
	}
	if got.OldValue == nil || *got.OldValue != "0.38.0" {
		t.Errorf("OldValue = %v, want 0.38.0", got.OldValue)
	}
	if got.NewValue == nil || *got.NewValue != "0.39.0" {
		t.Errorf("NewValue = %v, want 0.39.0", got.NewValue)
	}
	if got.Field != "subchart-versions" {
		t.Errorf("Field = %q, want subchart-versions", got.Field)
	}
}

// TestIssueRefsRoundTrip confirms that a Change's IssueRefs (issue/PR
// references parsed from its triggering commit message — see
// internal/issueref) persists and reads back intact through SaveChange ->
// QueryChangesets, and that a Change with no references round-trips to an empty
// slice — never a false/spurious reference (mirrors the Facets and Key
// round-trip contracts already proven above).
func TestIssueRefsRoundTrip(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	withRefs := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "versions.tf",
		Field:       "google-provider-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("5.0.0"),
		NewValue:    ptr("5.10.0"),
		CommitSha:   "sha-with-refs",
		Author:      "alice",
		CommittedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		IssueRefs:   []string{"#123", "ABC-456"},
	}
	withoutRefs := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "versions.tf",
		Field:       "google-provider-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("5.10.0"),
		NewValue:    ptr("5.11.0"),
		CommitSha:   "sha-without-refs",
		Author:      "bob",
		CommittedAt: time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC),
		IssueRefs:   nil,
	}

	if err := s.SaveChange(withRefs); err != nil {
		t.Fatalf("SaveChange (withRefs): %v", err)
	}
	if err := s.SaveChange(withoutRefs); err != nil {
		t.Fatalf("SaveChange (withoutRefs): %v", err)
	}

	feed := queryFeed(t, s)
	if len(feed) != 2 {
		t.Fatalf("queryFeed returned %d changes, want 2", len(feed))
	}

	// Newest first: sha-without-refs (later CommittedAt) is feed[0].
	if got := feed[0].IssueRefs; len(got) != 0 {
		t.Errorf("feed[0] (sha-without-refs) IssueRefs = %#v, want empty (no false reference)", got)
	}
	if got := feed[1].IssueRefs; len(got) != 2 || got[0] != "#123" || got[1] != "ABC-456" {
		t.Errorf("feed[1] (sha-with-refs) IssueRefs = %#v, want [\"#123\", \"ABC-456\"]", got)
	}
}

// TestSubjectRoundTrip confirms that a Change's Subject (the commit
// message's first line, see issue #85) persists and reads back intact
// through SaveChange -> QueryChangesets, and that a Change with no subject
// round-trips to an empty string — mirrors the IssueRefs and Facets
// round-trip contracts already proven above.
func TestSubjectRoundTrip(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	withSubject := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "versions.tf",
		Field:       "google-provider-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("5.0.0"),
		NewValue:    ptr("5.10.0"),
		CommitSha:   "sha-with-subject",
		Author:      "alice",
		CommittedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		Subject:     "bump google provider to 5.10.0",
	}
	withoutSubject := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "versions.tf",
		Field:       "google-provider-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("5.10.0"),
		NewValue:    ptr("5.11.0"),
		CommitSha:   "sha-without-subject",
		Author:      "bob",
		CommittedAt: time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC),
	}

	if err := s.SaveChange(withSubject); err != nil {
		t.Fatalf("SaveChange (withSubject): %v", err)
	}
	if err := s.SaveChange(withoutSubject); err != nil {
		t.Fatalf("SaveChange (withoutSubject): %v", err)
	}

	feed := queryFeed(t, s)
	if len(feed) != 2 {
		t.Fatalf("queryFeed returned %d changes, want 2", len(feed))
	}

	// Newest first: sha-without-subject (later CommittedAt) is feed[0].
	if got := feed[0].Subject; got != "" {
		t.Errorf("feed[0] (sha-without-subject) Subject = %q, want empty", got)
	}
	if got := feed[1].Subject; got != "bump google provider to 5.10.0" {
		t.Errorf("feed[1] (sha-with-subject) Subject = %q, want %q", got, "bump google provider to 5.10.0")
	}
}

// TestHighWaterMark_RoundTripsTheCursorTimestamp verifies the cursor's
// timestamp survives a write/read cycle. That timestamp is what lets a
// resuming poll bound its walk instead of walking to the repo root to find a
// SHA that carries no ordering (#180), so losing it silently costs
// performance rather than correctness — exactly the kind of regression a
// round-trip assertion is for.
func TestHighWaterMark_RoundTripsTheCursorTimestamp(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	// A timestamp with sub-second precision, to prove the stored format does
	// not truncate it: the walk bound compares against commit times exactly.
	at := time.Date(2024, 3, 15, 12, 30, 45, 123456789, time.UTC)

	if err := s.SetHighWaterMark("apps-repo", "Chart.yaml", "version", store.HighWaterMark{
		Sha: "sha-with-time", CommittedAt: at,
	}); err != nil {
		t.Fatalf("SetHighWaterMark: %v", err)
	}

	got, err := s.GetHighWaterMark("apps-repo", "Chart.yaml", "version")
	if err != nil {
		t.Fatalf("GetHighWaterMark: %v", err)
	}
	if got.Sha != "sha-with-time" {
		t.Errorf("Sha = %q, want sha-with-time", got.Sha)
	}
	if !got.CommittedAt.Equal(at) {
		t.Errorf("CommittedAt = %v, want %v", got.CommittedAt, at)
	}
	if !got.TimeKnown() {
		t.Error("TimeKnown() = false for a cursor written with a timestamp")
	}
}

// TestHighWaterMark_WithoutATimestampReadsBackAsUnknown covers the cursor
// whose time is not known: it is a usable cursor (the SHA is good) that simply
// cannot bound a walk. Distinguishing this from "no cursor at all" matters —
// the first still resumes, it just resumes the slow way.
func TestHighWaterMark_WithoutATimestampReadsBackAsUnknown(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	if err := s.SetHighWaterMark("apps-repo", "Chart.yaml", "version", store.HighWaterMark{
		Sha: "sha-no-time",
	}); err != nil {
		t.Fatalf("SetHighWaterMark: %v", err)
	}

	got, err := s.GetHighWaterMark("apps-repo", "Chart.yaml", "version")
	if err != nil {
		t.Fatalf("GetHighWaterMark: %v", err)
	}
	if got.IsZero() {
		t.Error("IsZero() = true, want false — the cursor's SHA is known and it must still resume")
	}
	if got.TimeKnown() {
		t.Errorf("TimeKnown() = true with CommittedAt %v, want false", got.CommittedAt)
	}
}

// TestHighWaterMark_UnsetReadsBackAsZero pins the first-run signal: a field
// that has never polled must be distinguishable from one holding a cursor, or
// it would skip its backfill.
func TestHighWaterMark_UnsetReadsBackAsZero(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	got, err := s.GetHighWaterMark("apps-repo", "never-polled.yaml", "version")
	if err != nil {
		t.Fatalf("GetHighWaterMark: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("IsZero() = false for an unset cursor (got %+v)", got)
	}
	if got.TimeKnown() {
		t.Error("TimeKnown() = true for an unset cursor")
	}
}

// TestHighWaterMark_AdvancingReplacesTheTimestamp verifies the SHA and its
// timestamp advance together. A stale timestamp paired with a fresh SHA would
// bound the next walk further back than necessary — harmless — but the
// reverse, a fresh timestamp with a stale SHA, would bound it past the cursor
// and hide history. They must move as one.
func TestHighWaterMark_AdvancingReplacesTheTimestamp(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	first := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	second := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	if err := s.SetHighWaterMark("r", "f", "x", store.HighWaterMark{Sha: "old", CommittedAt: first}); err != nil {
		t.Fatalf("SetHighWaterMark (first): %v", err)
	}
	if err := s.SetHighWaterMark("r", "f", "x", store.HighWaterMark{Sha: "new", CommittedAt: second}); err != nil {
		t.Fatalf("SetHighWaterMark (advance): %v", err)
	}

	got, err := s.GetHighWaterMark("r", "f", "x")
	if err != nil {
		t.Fatalf("GetHighWaterMark: %v", err)
	}
	if got.Sha != "new" || !got.CommittedAt.Equal(second) {
		t.Errorf("cursor = {%q, %v}, want {new, %v}", got.Sha, got.CommittedAt, second)
	}
}
