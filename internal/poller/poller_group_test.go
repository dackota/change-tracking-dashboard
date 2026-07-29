package poller_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/poller"
	"github.com/dackota/change-tracking-dashboard/internal/store"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// walkCommitsSpanName is the child span the poller wraps every
// gitsource.WalkCommits call in. Counting these spans is how these tests
// observe the number of history walks a poll cycle actually performs —
// the quantity issue #137 is about — without needing a fake git source.
const walkCommitsSpanName = "gitsource.walk_commits"

// multiFieldGlobRepo builds a repo where a glob matches TWO files and each
// file carries TWO independently-changing tracked fields:
//
//	c1: x{a:1,b:1}   c2: x{a:2,b:1}   c3: x{a:2,b:2}
//	c4: y{a:1,b:1}   c5: y{a:2,b:1}   c6: y{a:2,b:2}
//
// This is the shape issue #137 measures: one (repo, file-glob) fanned across
// N files with F fields, where the F fields all derive from the same N
// histories.
func multiFieldGlobRepo(t *testing.T) (repoPath string) {
	t.Helper()

	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var n int
	commit := func(relPath, a, b string) {
		full := filepath.Join(dir, relPath)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", relPath, err)
		}
		if err := os.WriteFile(full, []byte("a: "+a+"\nb: "+b+"\n"), 0o644); err != nil {
			t.Fatalf("write %q: %v", relPath, err)
		}
		if _, err := wt.Add(relPath); err != nil {
			t.Fatalf("git add %q: %v", relPath, err)
		}
		n++
		if _, err := wt.Commit("c"+relPath+a+b, &git.CommitOptions{
			Author: &object.Signature{Name: "dev", Email: "d@x.com",
				When: base.Add(time.Duration(n) * time.Hour)},
		}); err != nil {
			t.Fatalf("commit %q: %v", relPath, err)
		}
	}

	commit("app/x.yaml", "1", "1")
	commit("app/x.yaml", "2", "1")
	commit("app/x.yaml", "2", "2")
	commit("app/y.yaml", "1", "1")
	commit("app/y.yaml", "2", "1")
	commit("app/y.yaml", "2", "2")

	return dir
}

// groupTrackers returns the two-field tracker group for multiFieldGlobRepo.
func groupTrackers(repoPath string) []domain.Tracker {
	return []domain.Tracker{
		{Repo: repoPath, FileGlob: "app/*.yaml", Field: "field-a",
			ExtractorExpr: ".a", BackfillDays: 3650},
		{Repo: repoPath, FileGlob: "app/*.yaml", Field: "field-b",
			ExtractorExpr: ".b", BackfillDays: 3650},
	}
}

// countWalkSpans returns how many gitsource.WalkCommits child spans the
// exporter captured.
func countWalkSpans(exporter *tracetest.InMemoryExporter) int {
	var n int
	for _, s := range exporter.GetSpans() {
		if s.Name == walkCommitsSpanName {
			n++
		}
	}
	return n
}

// tracingPoller returns a Poller wired to an in-memory span exporter over a
// fresh store, plus the exporter and store.
func tracingPoller(t *testing.T, repoPath string) (*poller.Poller, *tracetest.InMemoryExporter, *store.Store) {
	t.Helper()

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	return poller.New(src, st, poller.WithTracerProvider(tp)), exporter, st
}

// feedKeys renders the store's feed as a stable, comparable set of
// field/path/old/new/sha tuples.
func feedKeys(t *testing.T, st *store.Store) []string {
	t.Helper()

	feed, err := st.QueryFeed(1000)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}
	deref := func(s *string) string {
		if s == nil {
			return "<nil>"
		}
		return *s
	}
	keys := make([]string, 0, len(feed))
	for _, c := range feed {
		keys = append(keys, c.FilePath+"|"+c.Field+"|"+deref(c.Key)+"|"+
			deref(c.OldValue)+"→"+deref(c.NewValue)+"|"+c.CommitSha)
	}
	sort.Strings(keys)
	return keys
}

// TestPollGroup_WalksEachFileOnce_NotOncePerField is the tracer bullet for
// issue #137: a tracker group sharing one (repo, file-glob) must walk each
// matched file's history exactly ONCE and extract every field from those
// shared snapshots. Before this change the poller walked per (file, field),
// so the live terraform/*.tf tracker performed 10 fields x 21 files = 210
// full-graph walks per cycle where 21 suffice.
func TestPollGroup_WalksEachFileOnce_NotOncePerField(t *testing.T) {
	t.Parallel()

	repoPath := multiFieldGlobRepo(t)
	p, exporter, _ := tracingPoller(t, repoPath)

	for i, err := range p.PollGroup(groupTrackers(repoPath)) {
		if err != nil {
			t.Fatalf("PollGroup[%d]: %v", i, err)
		}
	}

	// 2 matched files, 2 fields. One walk per file, not one per (file, field).
	if got := countWalkSpans(exporter); got != 2 {
		t.Errorf("WalkCommits calls = %d, want 2 (one per matched file)", got)
	}
}

// TestPollGroup_Incremental_WalksEachFileOnce covers the steady state, which
// is where the production CPU actually goes: on an incremental cycle the old
// per-field path walked TWICE per (file, field) — once for new commits and
// once unbounded to locate the high-water-mark commit's content.
func TestPollGroup_Incremental_WalksEachFileOnce(t *testing.T) {
	t.Parallel()

	repoPath := multiFieldGlobRepo(t)
	trackers := groupTrackers(repoPath)

	p, exporter, _ := tracingPoller(t, repoPath)
	for i, err := range p.PollGroup(trackers) {
		if err != nil {
			t.Fatalf("PollGroup (backfill)[%d]: %v", i, err)
		}
	}
	exporter.Reset()

	// A second cycle with no new commits still has to consult each file's
	// history to resolve its per-field HWMs — once per file, not per field.
	for i, err := range p.PollGroup(trackers) {
		if err != nil {
			t.Fatalf("PollGroup (incremental)[%d]: %v", i, err)
		}
	}

	if got := countWalkSpans(exporter); got != 2 {
		t.Errorf("incremental WalkCommits calls = %d, want 2 (one per matched file)", got)
	}
}

// TestPollGroup_WalkCountIsIndependentOfFieldCount asserts the invariant the
// whole change rests on: the number of history walks equals the number of
// matched files, for ANY field count and on both the backfill and incremental
// cycles. The old per-field path scaled as files x fields (x2 when
// incremental) — the production shape that made 10 fields cost 47.9 CPU-s.
func TestPollGroup_WalkCountIsIndependentOfFieldCount(t *testing.T) {
	t.Parallel()

	const matchedFiles = 2 // app/x.yaml, app/y.yaml

	for _, fields := range []int{1, 2, 5, 10} {
		t.Run(fmt.Sprintf("%d-fields", fields), func(t *testing.T) {
			t.Parallel()

			repoPath := multiFieldGlobRepo(t)
			trackers := make([]domain.Tracker, 0, fields)
			for i := range fields {
				// Alternate the two real keys so some fields see changes and
				// some see none; both must still share the one walk.
				expr := ".a"
				if i%2 == 1 {
					expr = ".b"
				}
				trackers = append(trackers, domain.Tracker{
					Repo: repoPath, FileGlob: "app/*.yaml",
					Field: fmt.Sprintf("field-%d", i), ExtractorExpr: expr,
					BackfillDays: 3650,
				})
			}

			p, exporter, _ := tracingPoller(t, repoPath)

			for _, cycle := range []string{"backfill", "incremental"} {
				exporter.Reset()
				for i, err := range p.PollGroup(trackers) {
					if err != nil {
						t.Fatalf("PollGroup (%s)[%d]: %v", cycle, i, err)
					}
				}
				if got := countWalkSpans(exporter); got != matchedFiles {
					t.Errorf("%s cycle: WalkCommits calls = %d, want %d (one per matched file, independent of the %d fields)",
						cycle, got, matchedFiles, fields)
				}
			}
		})
	}
}

// TestPollGroup_FeedIdenticalToPerFieldPolling is the behavioral safety net
// issue #137 demands: collapsing the walks must leave the recorded feed
// byte-identical to what per-tracker polling produced.
func TestPollGroup_FeedIdenticalToPerFieldPolling(t *testing.T) {
	t.Parallel()

	repoPath := multiFieldGlobRepo(t)
	trackers := groupTrackers(repoPath)

	perField, _, stPerField := tracingPoller(t, repoPath)
	for _, tr := range trackers {
		if err := perField.Poll(tr); err != nil {
			t.Fatalf("Poll(%s): %v", tr.Field, err)
		}
	}

	grouped, _, stGrouped := tracingPoller(t, repoPath)
	for i, err := range grouped.PollGroup(trackers) {
		if err != nil {
			t.Fatalf("PollGroup[%d]: %v", i, err)
		}
	}

	want, got := feedKeys(t, stPerField), feedKeys(t, stGrouped)
	if len(want) == 0 {
		t.Fatal("per-field baseline recorded no changes — fixture is not exercising the pipeline")
	}
	if len(want) != len(got) {
		t.Fatalf("feed size = %d, want %d\n got: %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("feed[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestPollGroup_PreservesPerFieldHighWaterMarks guards the #109 semantics the
// fix must not regress: fields in one group have INDEPENDENT cursors, so a
// field joining a group whose other fields are already caught up must still
// run its own full backfill.
func TestPollGroup_PreservesPerFieldHighWaterMarks(t *testing.T) {
	t.Parallel()

	repoPath := multiFieldGlobRepo(t)
	trackers := groupTrackers(repoPath)

	p, _, st := tracingPoller(t, repoPath)

	// field-a alone first: its cursor advances to HEAD, field-b's does not.
	if err := p.PollGroup(trackers[:1])[0]; err != nil {
		t.Fatalf("PollGroup(field-a only): %v", err)
	}
	// Now both. field-b must backfill its own history from scratch.
	for i, err := range p.PollGroup(trackers) {
		if err != nil {
			t.Fatalf("PollGroup(both)[%d]: %v", i, err)
		}
	}

	byField := map[string][]domain.Change{}
	feed, err := st.QueryFeed(1000)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}
	for _, c := range feed {
		byField[c.Field] = append(byField[c.Field], c)
	}

	// Each field changed once per file (1→2), across two files.
	for _, field := range []string{"field-a", "field-b"} {
		if got := len(byField[field]); got != 2 {
			t.Errorf("%s recorded %d changes, want 2 (one per matched file); feed: %v",
				field, got, feedKeys(t, st))
		}
	}
}

// TestPollGroup_PerTrackerErrors verifies errors are attributed per tracker,
// index-aligned with the input: one tracker's bad extractor expression must
// not be reported against its siblings, and must not stop them from
// recording their own changes.
func TestPollGroup_PerTrackerErrors(t *testing.T) {
	t.Parallel()

	repoPath := multiFieldGlobRepo(t)
	trackers := groupTrackers(repoPath)
	broken := domain.Tracker{Repo: repoPath, FileGlob: "app/*.yaml",
		Field: "field-broken", ExtractorExpr: ".a | totally_not_a_function",
		BackfillDays: 3650}

	p, _, st := tracingPoller(t, repoPath)
	errs := p.PollGroup([]domain.Tracker{trackers[0], broken, trackers[1]})

	if len(errs) != 3 {
		t.Fatalf("len(errs) = %d, want 3 (index-aligned with input)", len(errs))
	}
	if errs[0] != nil {
		t.Errorf("errs[0] (field-a) = %v, want nil", errs[0])
	}
	if errs[1] == nil {
		t.Error("errs[1] (field-broken) = nil, want an error")
	}
	if errs[2] != nil {
		t.Errorf("errs[2] (field-b) = %v, want nil", errs[2])
	}

	// The healthy siblings still recorded their changes.
	if got := len(feedKeys(t, st)); got != 4 {
		t.Errorf("feed has %d changes, want 4 (2 fields x 2 files): %v", got, feedKeys(t, st))
	}
}

// TestPollGroup_SingleTracker_MatchesPoll verifies Poll is exactly the
// one-tracker case of PollGroup — the two must not drift.
func TestPollGroup_SingleTracker_MatchesPoll(t *testing.T) {
	t.Parallel()

	repoPath := multiFieldGlobRepo(t)
	tracker := groupTrackers(repoPath)[0]

	viaPoll, _, stPoll := tracingPoller(t, repoPath)
	if err := viaPoll.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	viaGroup, _, stGroup := tracingPoller(t, repoPath)
	if err := viaGroup.PollGroup([]domain.Tracker{tracker})[0]; err != nil {
		t.Fatalf("PollGroup: %v", err)
	}

	want, got := feedKeys(t, stPoll), feedKeys(t, stGroup)
	if len(want) != len(got) {
		t.Fatalf("feed size = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("feed[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestPollGroup_Empty verifies the degenerate input: no trackers means no
// work, no walks, and no errors.
func TestPollGroup_Empty(t *testing.T) {
	t.Parallel()

	repoPath := multiFieldGlobRepo(t)
	p, exporter, _ := tracingPoller(t, repoPath)

	if errs := p.PollGroup(nil); len(errs) != 0 {
		t.Errorf("PollGroup(nil) = %v, want empty", errs)
	}
	if got := countWalkSpans(exporter); got != 0 {
		t.Errorf("WalkCommits calls = %d, want 0", got)
	}
}
