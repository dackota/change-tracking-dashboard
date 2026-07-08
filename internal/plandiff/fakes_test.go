package plandiff_test

import (
	"errors"
	"sync"

	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/plandiff"
)

// errUnexpected stands in for a generic, unclassified failure (neither a
// gitsource sentinel nor errBlockDepthExceeded) -- used to prove Diff folds
// any such error into the safe CouldNotRender bucket rather than leaking it
// or panicking.
var errUnexpected = errors.New("fakes_test: unexpected failure")

// fakePlanRepo is a plandiff.PlanRepo test double: both methods delegate to
// caller-supplied funcs, so each test configures exactly the git behavior it
// needs without a real repository. MaterializeSubtreeBounded calls are
// counted (thread-safely) so tests can assert "at most once per key" under
// concurrency, mirroring fakeParser's callCount.
type fakePlanRepo struct {
	firstParentFn func(sha string) (string, error)
	materializeFn func(sha, subtreePath, destDir string, bounds gitsource.MaterializeBounds) error

	mu             sync.Mutex
	materializeCns int
}

func (f *fakePlanRepo) FirstParent(sha string) (string, error) {
	return f.firstParentFn(sha)
}

func (f *fakePlanRepo) MaterializeSubtreeBounded(sha, subtreePath, destDir string, bounds gitsource.MaterializeBounds) error {
	f.mu.Lock()
	f.materializeCns++
	f.mu.Unlock()
	if f.materializeFn != nil {
		return f.materializeFn(sha, subtreePath, destDir, bounds)
	}
	return nil
}

// materializeCallCount returns the number of MaterializeSubtreeBounded calls
// observed so far.
func (f *fakePlanRepo) materializeCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.materializeCns
}

// fixedParentRepo returns a fakePlanRepo whose FirstParent always resolves
// to parentSha and whose MaterializeSubtreeBounded is a no-op success -- the
// common case for tests that only care about parse-side behavior.
func fixedParentRepo(parentSha string) *fakePlanRepo {
	return &fakePlanRepo{
		firstParentFn: func(string) (string, error) { return parentSha, nil },
	}
}

// fakeParser is a plandiff.Parser test double that counts invocations and
// delegates to a caller-supplied func, so tests can assert "the parser was
// invoked N times" (cache-hit / single-flight behavior) alongside
// controlling what each call returns.
type fakeParser struct {
	mu    sync.Mutex
	calls int
	fn    func(callN int, dir string) ([]plandiff.Resource, error)
}

func (f *fakeParser) Parse(dir string) ([]plandiff.Resource, error) {
	f.mu.Lock()
	f.calls++
	callN := f.calls
	f.mu.Unlock()
	if f.fn != nil {
		return f.fn(callN, dir)
	}
	return nil, nil
}

func (f *fakeParser) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// sequentialResourceParser returns a fakeParser that returns oldResources on
// its first invocation and newResources on every subsequent one -- matching
// Engine.compute's documented old-then-new parse order.
func sequentialResourceParser(oldResources, newResources []plandiff.Resource) *fakeParser {
	return &fakeParser{
		fn: func(callN int, _ string) ([]plandiff.Resource, error) {
			if callN == 1 {
				return oldResources, nil
			}
			return newResources, nil
		},
	}
}
