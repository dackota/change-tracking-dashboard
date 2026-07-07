// Regression test for the observability-foundation rework gap: on a poll
// failure, the scheduler used to log its own duplicate of the identical
// error via a hardcoded context.Background() (criterion 4) — right after
// poller.Poll's own ERROR log line for that same failure, which IS
// correlated to the poll cycle's trace/span. The checker live-reproduced
// this: one correlated line from the poller, immediately followed by one
// uncorrelated duplicate from the scheduler.
//
// The fix removes the scheduler's own log call entirely (Tick still feeds
// the error into the status recorder unchanged) rather than trying to
// correlate it: PollFunc's signature (func(domain.Tracker) error) carries no
// context, so the scheduler has no access to the poll cycle's actual
// trace/span — only the poll function's own implementation (poller.Poll)
// does.
//
// Caveat this test is honest about: the original bug's duplicate line was
// written via the package-wide default *slog.Logger, which is bound once
// at process-init time directly to os.Stderr — a sink no unit test can
// redirect after the fact (reassigning the os.Stderr variable doesn't
// change a writer that already captured its old value). So this test can't
// literally replay "old code produces 2 lines here, new code produces 1" —
// it instead locks in the now-correct contract (exactly one correlated
// ERROR line for the failure, landing wherever the poll function's own
// logger points, plus the status-recorder call unchanged) and would catch
// any future reintroduction of scheduler-side logging through an injectable
// path. Scheduler.go no longer imports "context" or "internal/telemetry" at
// all — the strongest available evidence that it cannot emit a stray line
// through this specific (hardcoded-sink) mechanism again.
package scheduler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/scheduler"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestScheduler_PollError_OnlyCorrelatedLogLineIsThePollersOwn(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := telemetry.NewLogger("change-tracking-dashboard", &buf)

	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tracer := tp.Tracer("test")

	wantErr := errors.New("clone failed: connection refused")

	// pollFn mirrors poller.Poll's own instrumentation: it starts its own
	// span for the poll cycle and logs the failure correlated to it. This is
	// the ONLY log line that should end up in buf — the scheduler must not
	// add a second, uncorrelated one for the same failure.
	var wantTraceID, wantSpanID string
	pollFn := func(tr domain.Tracker) error {
		ctx, span := tracer.Start(context.Background(), "poller.poll")
		defer span.End()
		wantTraceID = span.SpanContext().TraceID().String()
		wantSpanID = span.SpanContext().SpanID().String()

		correlated := telemetry.FromContext(ctx, logger)
		correlated.Error("poller: poll cycle failed", "repo", tr.Repo, "error", wantErr.Error())
		return wantErr
	}

	clk := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	status := &fakeStatusRecorder{}
	tr := makeTracker("/repo/a", "Chart.yaml", "version", 60)

	sched := scheduler.New(clk.Now, scheduler.PollFunc(pollFn), status)
	sched.Tick([]domain.Tracker{tr})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("expected exactly 1 log line for the poll failure (the poller's own correlated line), got %d: %q", len(lines), buf.String())
	}

	var fields map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &fields); err != nil {
		t.Fatalf("log line not JSON: %v; line: %s", err, lines[0])
	}
	if fields["trace_id"] != wantTraceID {
		t.Errorf("trace_id = %v, want %s", fields["trace_id"], wantTraceID)
	}
	if fields["span_id"] != wantSpanID {
		t.Errorf("span_id = %v, want %s", fields["span_id"], wantSpanID)
	}
	if fields["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", fields["level"])
	}

	// The error must still reach the status recorder even though the
	// scheduler itself no longer logs it directly (Record is the seam
	// pollstatus relies on for LastError/LastSuccessAt).
	calls := status.snapshot()
	if len(calls) != 1 {
		t.Fatalf("status recorder calls = %d, want 1", len(calls))
	}
	if !errors.Is(calls[0].err, wantErr) {
		t.Errorf("status recorder err = %v, want %v", calls[0].err, wantErr)
	}
}
