package chartdiff_test

import (
	"errors"
	"sync"

	"github.com/dackota/change-tracking-dashboard/internal/chartrender"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
)

// errUnexpected stands in for a generic, unclassified failure (neither a
// gitsource sentinel nor a *chartrender.Failure) — used to prove Diff folds
// any such error into the safe CouldNotRender bucket rather than leaking it
// or panicking.
var errUnexpected = errors.New("fakes_test: unexpected failure")

// fakeChartRepo is a chartdiff.ChartRepo test double: both methods delegate
// to caller-supplied funcs, so each test configures exactly the git behavior
// it needs without a real repository. MaterializeSubtreeBounded calls are
// counted (thread-safely) so tests can assert "at most once per key" under
// concurrency, mirroring fakeRenderer's callCount.
type fakeChartRepo struct {
	firstParentFn func(sha string) (string, error)
	materializeFn func(sha, subtreePath, destDir string, bounds gitsource.MaterializeBounds) error

	mu             sync.Mutex
	materializeCns int
}

func (f *fakeChartRepo) FirstParent(sha string) (string, error) {
	return f.firstParentFn(sha)
}

func (f *fakeChartRepo) MaterializeSubtreeBounded(sha, subtreePath, destDir string, bounds gitsource.MaterializeBounds) error {
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
func (f *fakeChartRepo) materializeCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.materializeCns
}

// fixedParentRepo returns a fakeChartRepo whose FirstParent always resolves
// to parentSha and whose MaterializeSubtreeBounded is a no-op success — the
// common case for tests that only care about render-side behavior.
func fixedParentRepo(parentSha string) *fakeChartRepo {
	return &fakeChartRepo{
		firstParentFn: func(string) (string, error) { return parentSha, nil },
	}
}

// fakeRenderer is a chartdiff.Renderer test double that counts invocations
// and delegates to a caller-supplied func, so tests can assert "the renderer
// was invoked N times" (cache-hit / single-flight behavior) alongside
// controlling what each call returns.
type fakeRenderer struct {
	mu    sync.Mutex
	calls int
	fn    func(callN int, chartDir string, values map[string]interface{}) (*chartrender.Result, error)
}

func (f *fakeRenderer) Render(chartDir string, values map[string]interface{}) (*chartrender.Result, error) {
	f.mu.Lock()
	f.calls++
	callN := f.calls
	f.mu.Unlock()
	if f.fn != nil {
		return f.fn(callN, chartDir, values)
	}
	return &chartrender.Result{}, nil
}

func (f *fakeRenderer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// sequentialManifestRenderer returns a fakeRenderer that returns oldResult on
// its first invocation and newResult on every subsequent one — matching
// Engine.compute's documented old-then-new render order.
func sequentialManifestRenderer(oldResult, newResult *chartrender.Result) *fakeRenderer {
	return &fakeRenderer{
		fn: func(callN int, _ string, _ map[string]interface{}) (*chartrender.Result, error) {
			if callN == 1 {
				return oldResult, nil
			}
			return newResult, nil
		},
	}
}
