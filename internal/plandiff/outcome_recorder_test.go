package plandiff_test

import (
	"context"
	"sync"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/plandiff"
)

// spyOutcomeRecorder is a plandiff.OutcomeRecorder test double that counts
// how many times each Kind string was reported.
type spyOutcomeRecorder struct {
	mu     sync.Mutex
	counts map[string]int
}

func newSpyOutcomeRecorder() *spyOutcomeRecorder {
	return &spyOutcomeRecorder{counts: make(map[string]int)}
}

func (s *spyOutcomeRecorder) RecordPlanDiffOutcome(kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts[kind]++
}

func (s *spyOutcomeRecorder) count(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[kind]
}

// TestDiff_WithOutcomeRecorder_ReportsEveryOutcomeKind proves acceptance
// criterion 9's engine-side half: every Diff call reports its Outcome's Kind
// to the configured OutcomeRecorder, including a cache hit (the second call
// for the same key never recomputes, but must still report).
func TestDiff_WithOutcomeRecorder_ReportsEveryOutcomeKind(t *testing.T) {
	t.Parallel()

	recorder := newSpyOutcomeRecorder()
	repo := fixedParentRepo("parent-sha")
	parser := sequentialResourceParser(nil, nil)

	engine, err := plandiff.NewEngine(plandiff.Config{}, parser, plandiff.WithOutcomeRecorder(recorder))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	req := plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "sha"}
	engine.Diff(context.Background(), repo, req) // genuine compute
	engine.Diff(context.Background(), repo, req) // cache hit

	if got := recorder.count("ok"); got != 2 {
		t.Errorf(`recorder.count("ok") = %d, want 2 (both the computed and cache-hit call report)`, got)
	}
}

// TestDiff_WithOutcomeRecorder_ReportsFailureKinds proves non-OK outcomes
// (including NoPriorVersion, which is not cached) are reported too.
func TestDiff_WithOutcomeRecorder_ReportsFailureKinds(t *testing.T) {
	t.Parallel()

	recorder := newSpyOutcomeRecorder()
	repo := &fakePlanRepo{firstParentFn: func(string) (string, error) { return "", gitsource.ErrNoParent }}
	parser := &fakeParser{}

	engine, err := plandiff.NewEngine(plandiff.Config{}, parser, plandiff.WithOutcomeRecorder(recorder))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "root"})
	engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "root"})

	if got := recorder.count(string(plandiff.NoPriorVersion)); got != 2 {
		t.Errorf("recorder.count(NoPriorVersion) = %d, want 2", got)
	}
}

// TestDiff_WithoutOutcomeRecorder_DoesNotPanic proves the noop default is
// safe: an Engine built without WithOutcomeRecorder never nil-derefs.
func TestDiff_WithoutOutcomeRecorder_DoesNotPanic(t *testing.T) {
	t.Parallel()

	engine, err := plandiff.NewEngine(plandiff.Config{}, &fakeParser{})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), fixedParentRepo("parent-sha"), plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "sha"})
	if outcome.Kind != plandiff.OK {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, plandiff.OK)
	}
}
