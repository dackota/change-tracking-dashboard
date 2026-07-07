// Package pollstatus_test exercises the Registry through its public
// Record/Snapshot interface only.
package pollstatus_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/pollstatus"
)

// makeTracker builds a minimal domain.Tracker with the given poll interval,
// mirroring the helper scheduler_test.go uses.
func makeTracker(repo, glob, field string, pollSecs int) domain.Tracker {
	return domain.Tracker{
		Repo:                repo,
		FileGlob:            glob,
		Field:               field,
		PollIntervalSeconds: pollSecs,
	}
}

// statusFor returns the TrackerStatus for tr within snap, failing the test if
// not found.
func statusFor(t *testing.T, snap []pollstatus.TrackerStatus, tr domain.Tracker) pollstatus.TrackerStatus {
	t.Helper()
	for _, ts := range snap {
		if ts.Repo == tr.Repo && ts.FileGlob == tr.FileGlob && ts.Field == tr.Field {
			return ts
		}
	}
	t.Fatalf("Snapshot() has no entry for tracker %+v", tr)
	return pollstatus.TrackerStatus{}
}

// --- Behavior 1: a successful poll records the attempt and success times ---

func TestRegistry_RecordSuccess_SetsLastAttemptAndLastSuccess(t *testing.T) {
	t.Parallel()

	reg := pollstatus.New()
	tr := makeTracker("/repo/a", "Chart.yaml", "version", 60)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	reg.Record(tr, at, nil)

	got := statusFor(t, reg.Snapshot(), tr)
	if !got.LastAttemptAt.Equal(at) {
		t.Errorf("LastAttemptAt = %v, want %v", got.LastAttemptAt, at)
	}
	if !got.LastSuccessAt.Equal(at) {
		t.Errorf("LastSuccessAt = %v, want %v", got.LastSuccessAt, at)
	}
	if got.LastError != "" {
		t.Errorf("LastError = %q, want empty on success", got.LastError)
	}
}

// --- Behavior 2: a failed poll sets LastError and leaves LastSuccessAt unchanged ---

func TestRegistry_RecordFailure_SetsLastErrorAndLeavesLastSuccessUnchanged(t *testing.T) {
	t.Parallel()

	reg := pollstatus.New()
	tr := makeTracker("/repo/a", "Chart.yaml", "version", 60)

	successAt := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	reg.Record(tr, successAt, nil)

	failAt := successAt.Add(60 * time.Second)
	pollErr := errors.New("clone failed: connection refused")
	reg.Record(tr, failAt, pollErr)

	got := statusFor(t, reg.Snapshot(), tr)
	if !got.LastAttemptAt.Equal(failAt) {
		t.Errorf("LastAttemptAt = %v, want %v (advances on every Record)", got.LastAttemptAt, failAt)
	}
	if got.LastError != pollErr.Error() {
		t.Errorf("LastError = %q, want %q", got.LastError, pollErr.Error())
	}
	if !got.LastSuccessAt.Equal(successAt) {
		t.Errorf("LastSuccessAt = %v, want unchanged at %v (last success)", got.LastSuccessAt, successAt)
	}
}

// --- Behavior 3: a later success after a failure clears LastError and advances LastSuccessAt ---

func TestRegistry_LaterSuccessAfterFailure_ClearsErrorAndAdvancesSuccess(t *testing.T) {
	t.Parallel()

	reg := pollstatus.New()
	tr := makeTracker("/repo/a", "Chart.yaml", "version", 60)

	failAt := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	reg.Record(tr, failAt, errors.New("network error"))

	successAt := failAt.Add(60 * time.Second)
	reg.Record(tr, successAt, nil)

	got := statusFor(t, reg.Snapshot(), tr)
	if got.LastError != "" {
		t.Errorf("LastError = %q, want empty after a later success", got.LastError)
	}
	if !got.LastSuccessAt.Equal(successAt) {
		t.Errorf("LastSuccessAt = %v, want %v", got.LastSuccessAt, successAt)
	}
	if !got.LastAttemptAt.Equal(successAt) {
		t.Errorf("LastAttemptAt = %v, want %v", got.LastAttemptAt, successAt)
	}
}

// --- Behavior 4: NextRunAt == LastAttemptAt + the tracker's interval ---

func TestRegistry_NextRunAt_EqualsLastAttemptPlusInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		pollSecs int
	}{
		{"typical interval", 60},
		{"long interval", 3600},
		{"zero interval fires again immediately", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := pollstatus.New()
			tr := makeTracker("/repo/a", "Chart.yaml", "version", tt.pollSecs)
			at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

			reg.Record(tr, at, nil)

			got := statusFor(t, reg.Snapshot(), tr)
			want := at.Add(time.Duration(tt.pollSecs) * time.Second)
			if !got.NextRunAt.Equal(want) {
				t.Errorf("NextRunAt = %v, want %v", got.NextRunAt, want)
			}
		})
	}
}

// --- Behavior 5: a fresh Registry's Snapshot is empty (R10: rebuilds naturally, no persistence) ---

func TestRegistry_FreshRegistry_SnapshotIsEmpty(t *testing.T) {
	t.Parallel()

	reg := pollstatus.New()

	got := reg.Snapshot()
	if len(got) != 0 {
		t.Errorf("Snapshot() on a fresh Registry = %+v, want empty (no persistence — state rebuilds from Record calls only)", got)
	}
}

// --- Behavior 6: Snapshot orders trackers deterministically by (Repo, FileGlob, Field) ---

func TestRegistry_Snapshot_DeterministicOrder(t *testing.T) {
	t.Parallel()

	reg := pollstatus.New()
	trC := makeTracker("/repo/c", "Chart.yaml", "version", 60)
	trA := makeTracker("/repo/a", "Chart.yaml", "version", 60)
	trB := makeTracker("/repo/b", "Chart.yaml", "version", 60)

	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	// Record in a deliberately non-sorted order.
	reg.Record(trC, at, nil)
	reg.Record(trA, at, nil)
	reg.Record(trB, at, nil)

	want := []string{trA.Repo, trB.Repo, trC.Repo}
	for i := 0; i < 3; i++ {
		got := reg.Snapshot()
		if len(got) != 3 {
			t.Fatalf("Snapshot() len = %d, want 3", len(got))
		}
		for j, ts := range got {
			if ts.Repo != want[j] {
				t.Errorf("call %d: Snapshot()[%d].Repo = %q, want %q (sorted order)", i, j, ts.Repo, want[j])
			}
		}
	}
}

// --- Behavior 7: Snapshot returns a copy that shares no mutable state with the Registry ---

func TestRegistry_Snapshot_ReturnsIndependentCopy(t *testing.T) {
	t.Parallel()

	reg := pollstatus.New()
	tr := makeTracker("/repo/a", "Chart.yaml", "version", 60)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	reg.Record(tr, at, nil)

	got := reg.Snapshot()
	// Mutate the caller's copy — this must never be visible through the
	// Registry's internal state or a subsequent Snapshot call.
	got[0].Repo = "mutated"
	got[0].LastError = "mutated"
	got[0].LastAttemptAt = at.Add(999 * time.Hour)

	again := statusFor(t, reg.Snapshot(), tr)
	if again.Repo != tr.Repo {
		t.Errorf("Repo = %q after caller mutated its copy, want unaffected %q", again.Repo, tr.Repo)
	}
	if again.LastError != "" {
		t.Errorf("LastError = %q after caller mutated its copy, want unaffected empty string", again.LastError)
	}
	if !again.LastAttemptAt.Equal(at) {
		t.Errorf("LastAttemptAt = %v after caller mutated its copy, want unaffected %v", again.LastAttemptAt, at)
	}
}
