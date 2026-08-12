package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
)

// logLine is the subset of a captured slog JSON record these tests assert on.
// The message key is slog's default "msg", not the "message" the production
// logger renames it to — telemetry.NewLogger owns that rename, and these tests
// capture through a plain handler so they can set LevelDebug.
type logLine struct {
	Level   string `json:"level"`
	Message string `json:"msg"`
	Error   string `json:"error"`
	Repo    string `json:"repo"`
}

// captureCtx returns a context carrying a logger that writes JSON to buf at
// LevelDebug. The level matters: the production logger sits at LevelInfo, so a
// Debug-classified line is dropped entirely there — these tests turn the level
// up specifically to observe *which* level the helper chose rather than only
// that the line vanished.
func captureCtx(buf *bytes.Buffer) context.Context {
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return telemetry.ContextWithLogger(context.Background(), logger)
}

// decodeLines parses every JSON record written to buf.
func decodeLines(t *testing.T, buf *bytes.Buffer) []logLine {
	t.Helper()

	var out []logLine
	scanner := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var line logLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("unmarshal log line %q: %v", scanner.Text(), err)
		}
		out = append(out, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan log buffer: %v", err)
	}
	return out
}

// netOpError builds the error shape a real mid-response write failure has: the
// net package wraps the raw errno in *net.OpError via *os.SyscallError, and
// html/template surfaces that verbatim out of Execute. This is the exact shape
// behind the production log line
// "write tcp 10.127.84.17:8080->10.127.83.195:58478: write: broken pipe".
func netOpError(errno syscall.Errno) error {
	return &net.OpError{
		Op:  "write",
		Net: "tcp",
		Err: os.NewSyscallError("write", errno),
	}
}

// TestLogResponseWriteError_LevelByCause verifies the classification: a client
// that hangs up mid-response is not an application fault and must not log at
// ERROR, while a genuine write/render failure still must.
func TestLogResponseWriteError_LevelByCause(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       error
		wantLevel string
	}{
		{"bare EPIPE", syscall.EPIPE, "DEBUG"},
		{"bare ECONNRESET", syscall.ECONNRESET, "DEBUG"},
		{"net.OpError wrapping EPIPE", netOpError(syscall.EPIPE), "DEBUG"},
		{"net.OpError wrapping ECONNRESET", netOpError(syscall.ECONNRESET), "DEBUG"},
		{"use of closed network connection", net.ErrClosed, "DEBUG"},
		{"client cancelled the request", context.Canceled, "DEBUG"},
		{"closed pipe", io.ErrClosedPipe, "DEBUG"},
		{"template execution failure", errors.New("template: timeline: nil pointer evaluating"), "ERROR"},
		{"disk full", syscall.ENOSPC, "ERROR"},
		{"deadline exceeded is a server-side timeout", context.DeadlineExceeded, "ERROR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			buf := &bytes.Buffer{}
			logResponseWriteError(captureCtx(buf), "web: render timeline template", tt.err)

			lines := decodeLines(t, buf)
			if len(lines) != 1 {
				t.Fatalf("got %d log lines, want exactly 1: %q", len(lines), buf.String())
			}
			if lines[0].Level != tt.wantLevel {
				t.Errorf("level = %q, want %q (err %v)", lines[0].Level, tt.wantLevel, tt.err)
			}
			if lines[0].Message != "web: render timeline template" {
				t.Errorf("message = %q, want the caller's message unchanged", lines[0].Message)
			}
			if lines[0].Error != tt.err.Error() {
				t.Errorf("error attr = %q, want %q — the cause must stay observable at either level", lines[0].Error, tt.err.Error())
			}
		})
	}
}

// TestLogResponseWriteError_ClientDisconnectNeverLogsErrorAtAnyWrapDepth
// asserts the invariant over the whole class rather than the two shapes
// observed in production: however many layers of %w sit between the handler
// and the errno, a client disconnect must never reach ERROR. Handlers wrap
// freely, so a depth-sensitive check would silently regress the moment one of
// them added context to the error.
func TestLogResponseWriteError_ClientDisconnectNeverLogsErrorAtAnyWrapDepth(t *testing.T) {
	t.Parallel()

	disconnects := []error{
		syscall.EPIPE,
		syscall.ECONNRESET,
		netOpError(syscall.EPIPE),
		netOpError(syscall.ECONNRESET),
		net.ErrClosed,
		context.Canceled,
	}

	for _, base := range disconnects {
		for depth := range 6 {
			err := base
			for i := range depth {
				err = fmt.Errorf("layer %d: %w", i, err)
			}

			buf := &bytes.Buffer{}
			logResponseWriteError(captureCtx(buf), "web: render", err)

			lines := decodeLines(t, buf)
			if len(lines) != 1 {
				t.Fatalf("base %v depth %d: got %d log lines, want 1", base, depth, len(lines))
			}
			if lines[0].Level == "ERROR" {
				t.Errorf("base %v wrapped %d deep logged at ERROR; want DEBUG", base, depth)
			}
		}
	}
}

// TestLogResponseWriteError_NilErrorLogsNothing covers the adversarial input a
// caller can hand this helper by mistake: there is no failure to report, so
// there must be no line at either level.
func TestLogResponseWriteError_NilErrorLogsNothing(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	logResponseWriteError(captureCtx(buf), "web: render timeline template", nil)

	if lines := decodeLines(t, buf); len(lines) != 0 {
		t.Errorf("got %d log lines for a nil error, want 0: %q", len(lines), buf.String())
	}
}

// TestLogResponseWriteError_PreservesCallerAttrs verifies the extra key/values
// the chart-diff and plan-diff sites already log (repo, tenant, commitSha)
// survive the move to this helper — losing them would strip the only context
// that makes those failures diagnosable.
func TestLogResponseWriteError_PreservesCallerAttrs(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		err  error
	}{
		{"disconnect", netOpError(syscall.EPIPE)},
		{"genuine failure", errors.New("boom")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			buf := &bytes.Buffer{}
			logResponseWriteError(captureCtx(buf), "web: render chart diff", tt.err, "repo", "infra")

			lines := decodeLines(t, buf)
			if len(lines) != 1 {
				t.Fatalf("got %d log lines, want 1", len(lines))
			}
			if lines[0].Repo != "infra" {
				t.Errorf("repo attr = %q, want %q", lines[0].Repo, "infra")
			}
		})
	}
}
