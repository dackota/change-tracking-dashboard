package subtree_test

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/subtree"
)

// errUnexpected stands in for a generic, unclassified failure — used to prove
// the engine folds any such error into a Failure the domain classifies,
// rather than leaking it or panicking.
var errUnexpected = errors.New("subtree_test: unexpected failure")

// --- the test domain -------------------------------------------------------

// outcome is the test domain's Outcome type. The engine treats it as opaque:
// it caches and returns it without ever inspecting it, which is exactly the
// property these tests exercise.
type outcome struct {
	Kind string
	Old  []string
	New  []string
	// Cause is the produce error the domain saw, so tests can assert the
	// engine handed the domain's own error back unwrapped.
	Cause error
}

const (
	kindOK             = "ok"
	kindNoPriorVersion = "no-prior-version"
	kindExceeded       = "exceeded-limits"
	kindFailed         = "failed"
)

// fakeDomain is a subtree.Domain[string, outcome] test double: Produce
// delegates to a caller-supplied func and counts invocations, so tests can
// assert "Produce ran at most once per key" (cache / single-flight behavior)
// alongside controlling what each call returns.
type fakeDomain struct {
	mu    sync.Mutex
	calls int
	dirs  []string

	fn func(callN int, dir string) ([]string, error)
}

func (d *fakeDomain) Produce(dir string) ([]string, error) {
	d.mu.Lock()
	d.calls++
	callN := d.calls
	d.dirs = append(d.dirs, dir)
	d.mu.Unlock()
	if d.fn != nil {
		return d.fn(callN, dir)
	}
	return []string{dir}, nil
}

func (d *fakeDomain) Combine(old, new []string) outcome {
	return outcome{Kind: kindOK, Old: old, New: new}
}

func (d *fakeDomain) Classify(_ context.Context, f subtree.Failure) outcome {
	switch f.Kind {
	case subtree.NoPriorVersion:
		return outcome{Kind: kindNoPriorVersion}
	case subtree.ExceededLimits:
		return outcome{Kind: kindExceeded}
	case subtree.ProduceFailed:
		return outcome{Kind: kindFailed, Cause: f.Cause}
	}
	return outcome{Kind: kindFailed}
}

func (d *fakeDomain) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// observedDirs returns the directories Produce was handed, in call order.
func (d *fakeDomain) observedDirs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.dirs...)
}

// --- the test repo ---------------------------------------------------------

// fakeRepo is a subtree.Repo test double: both methods delegate to
// caller-supplied funcs, so each test configures exactly the git behavior it
// needs without a real repository. MaterializeSubtreeBounded calls are
// counted (thread-safely) and the bounds it was handed are recorded, so tests
// can assert both "at most once per key" and "the configured bounds reached
// the repo".
type fakeRepo struct {
	firstParentFn func(sha string) (string, error)
	materializeFn func(sha, subtreePath, destDir string, bounds gitsource.MaterializeBounds) error

	mu             sync.Mutex
	materializeCns int
	lastBounds     gitsource.MaterializeBounds
}

func (r *fakeRepo) FirstParent(sha string) (string, error) {
	if r.firstParentFn != nil {
		return r.firstParentFn(sha)
	}
	return "parent-sha", nil
}

func (r *fakeRepo) MaterializeSubtreeBounded(sha, subtreePath, destDir string, bounds gitsource.MaterializeBounds) error {
	r.mu.Lock()
	r.materializeCns++
	r.lastBounds = bounds
	r.mu.Unlock()
	if r.materializeFn != nil {
		return r.materializeFn(sha, subtreePath, destDir, bounds)
	}
	return nil
}

func (r *fakeRepo) materializeCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.materializeCns
}

func (r *fakeRepo) boundsSeen() gitsource.MaterializeBounds {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastBounds
}

// fixedParentRepo returns a fakeRepo whose FirstParent always resolves to
// parentSha and whose MaterializeSubtreeBounded is a no-op success — the
// common case for tests that only care about produce-side behavior.
func fixedParentRepo(parentSha string) *fakeRepo {
	return &fakeRepo{firstParentFn: func(string) (string, error) { return parentSha, nil }}
}

// --- construction helpers --------------------------------------------------

// testConfig returns a resolved subtree.Config with generous bounds and the
// labels every test asserts against. Individual tests override the one or two
// fields they are exercising.
func testConfig() subtree.Config {
	return subtree.Config{
		ProduceTimeout:            10 * time.Second,
		ConcurrencyCap:            4,
		CacheEntries:              128,
		MaterializeTimeout:        10 * time.Second,
		MaterializeConcurrencyCap: 4,
		Materialize: gitsource.MaterializeBounds{
			MaxTotalBytes: 64 << 20,
			MaxFiles:      2000,
			MaxDepth:      20,
			MaxTreeNodes:  5000,
		},
		Labels: subtree.Labels{
			Name:                "testdiff",
			ProduceVerb:         "produce",
			ProduceSpanName:     "testdiff.produce",
			InstrumentationName: "github.com/dackota/change-tracking-dashboard/internal/subtree_test",
		},
	}
}

// newTestEngine builds an Engine over domain with cfg, failing the test on a
// construction error.
func newTestEngine(t testingT, cfg subtree.Config, domain *fakeDomain, opts ...subtree.Option) *subtree.Engine[string, outcome] {
	t.Helper()
	e, err := subtree.NewEngine[string, outcome](cfg, domain, opts...)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

// testingT is the slice of *testing.T these helpers need, so they can also be
// used from a quick.Check property body.
type testingT interface {
	Helper()
	Fatalf(format string, args ...any)
}

// defaultRequest is the well-formed request most tests reuse.
var defaultRequest = subtree.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha"}
