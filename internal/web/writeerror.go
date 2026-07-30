// Package web (this file): the single chokepoint every handler routes a failed
// response-body write through, so that "the client hung up" and "this handler
// is broken" do not land at the same log level.
//
// The motivating incident: the Kubernetes liveness and readiness probes were
// pointed at / (the full timeline page). kubelet reads a probe response's
// status line and closes the connection without draining the body, so every
// probe aborted the template write mid-body and the timeline handler logged
//
//	ERROR web: render timeline template
//	  error: write tcp 10.127.84.17:8080->10.127.83.195:58478: write: broken pipe
//
// several times a minute on a pod that was entirely healthy. Repointing the
// probes at /healthz stopped that particular flood, but the underlying
// misclassification is not probe-specific: a user who navigates away from a
// slow page load produces the identical write error, and it is not an
// application fault either time. There is also nothing a handler can do about
// it — by the time the write fails the status code is already on the wire, so
// the only decision left is what to log.
package web

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"

	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
)

// logResponseWriteError reports a failed response write at a level reflecting
// who caused it: DEBUG when the client went away (nothing is broken and
// nothing is actionable), ERROR otherwise. The cause stays on the record at
// either level, so turning the log level up recovers the full detail.
//
// attrs are extra key/value pairs appended before the error, matching the
// order the chart-diff and plan-diff sites already used.
//
// A nil err logs nothing: there is no failure to report.
func logResponseWriteError(ctx context.Context, msg string, err error, attrs ...any) {
	if err == nil {
		return
	}

	args := make([]any, 0, len(attrs)+2)
	args = append(args, attrs...)
	args = append(args, "error", err)

	logger := telemetry.LoggerFromContext(ctx)
	if isClientDisconnect(err) {
		logger.Debug(msg, args...)
		return
	}
	logger.Error(msg, args...)
}

// isClientDisconnect reports whether err is the peer going away mid-response
// rather than a server-side failure.
//
// Matching is done with errors.Is at every layer because handlers wrap freely
// and the shape varies by seam: html/template surfaces the raw *net.OpError
// out of Execute, while net/http can report the same disconnect as
// net.ErrClosed or as a cancelled request context.
//
// context.DeadlineExceeded is deliberately absent — that is a server-side
// timeout the operator does want to see.
func isClientDisconnect(err error) bool {
	switch {
	case errors.Is(err, syscall.EPIPE),
		errors.Is(err, syscall.ECONNRESET),
		errors.Is(err, net.ErrClosed),
		errors.Is(err, io.ErrClosedPipe),
		errors.Is(err, context.Canceled):
		return true
	}
	return false
}
