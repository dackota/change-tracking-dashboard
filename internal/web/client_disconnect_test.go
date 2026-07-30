package web_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/pollstatus"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// disconnectingWriter is a ResponseWriter whose body writes fail the way a
// hung-up client's do: the kubelet HTTP probe reads the status line and closes
// the connection without draining the body, so the first body write returns
// ECONNRESET/EPIPE wrapped in *net.OpError.
type disconnectingWriter struct {
	header http.Header
	err    error
}

func newDisconnectingWriter(errno syscall.Errno) *disconnectingWriter {
	return &disconnectingWriter{
		header: http.Header{},
		err: &net.OpError{
			Op:  "write",
			Net: "tcp",
			Err: os.NewSyscallError("write", errno),
		},
	}
}

func (w *disconnectingWriter) Header() http.Header       { return w.header }
func (w *disconnectingWriter) WriteHeader(int)           {}
func (w *disconnectingWriter) Write([]byte) (int, error) { return 0, w.err }

// capturedLevels returns the level of every log record in buf whose message
// matches want.
func capturedLevels(t *testing.T, buf *bytes.Buffer, wantMessage string) []string {
	t.Helper()

	var levels []string
	scanner := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var line struct {
			Level   string `json:"level"`
			Message string `json:"msg"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("unmarshal log line %q: %v", scanner.Text(), err)
		}
		if line.Message == wantMessage {
			levels = append(levels, line.Level)
		}
	}
	return levels
}

// TestHandlers_ClientDisconnectMidRenderDoesNotLogError is the regression test
// for the probe-induced ERROR flood: probes hit / every 10-15s, kubelet closed
// each connection before the template finished writing, and every one of those
// aborted writes surfaced as
//
//	ERROR web: render timeline template
//	  error: write tcp ...: write: connection reset by peer
//
// on an otherwise perfectly healthy pod. The probe target is fixed separately
// in the gitops repo; this asserts the app no longer reports a client hanging
// up as an application fault, since a real user navigating away mid-load
// produces the identical write error.
func TestHandlers_ClientDisconnectMidRenderDoesNotLogError(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	handlers := []struct {
		name    string
		target  string
		message string
		handler http.Handler
	}{
		{
			name:    "timeline",
			target:  "/",
			message: "web: render timeline template",
			handler: web.NewTimelineHandler(st, pollstatus.New()),
		},
		{
			name:    "healthz",
			target:  "/healthz",
			message: "web: write healthz response",
			handler: web.NewHealthzHandler(),
		},
	}

	for _, h := range handlers {
		for _, errno := range []syscall.Errno{syscall.EPIPE, syscall.ECONNRESET} {
			t.Run(h.name+"/"+errnoName(errno), func(t *testing.T) {
				t.Parallel()

				buf := &bytes.Buffer{}
				logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
				ctx := telemetry.ContextWithLogger(context.Background(), logger)

				req := httptest.NewRequest(http.MethodGet, h.target, nil).WithContext(ctx)
				h.handler.ServeHTTP(newDisconnectingWriter(errno), req)

				levels := capturedLevels(t, buf, h.message)
				for _, level := range levels {
					if level == "ERROR" {
						t.Errorf("%s logged %q at ERROR on client disconnect (%v); want DEBUG\n%s",
							h.name, h.message, errno, buf.String())
					}
				}
				if strings.Contains(buf.String(), `"level":"ERROR"`) {
					t.Errorf("%s emitted an ERROR line on client disconnect (%v):\n%s", h.name, errno, buf.String())
				}
			})
		}
	}
}

// errnoName labels subtests without depending on the platform's strerror text.
func errnoName(errno syscall.Errno) string {
	switch errno {
	case syscall.EPIPE:
		return "EPIPE"
	case syscall.ECONNRESET:
		return "ECONNRESET"
	default:
		return "other"
	}
}
