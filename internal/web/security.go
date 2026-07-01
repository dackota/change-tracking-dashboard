// Package web (this file): security headers shared by every response the
// dashboard serves (the timeline page, its embedded static asset, and the
// JSON API). Kept in one place so the security posture — CSP, frame/sniff
// protections, referrer policy — cannot silently drift between handlers.
package web

import "net/http"

// contentSecurityPolicy permits exactly one first-party script (the
// embedded, vendored timeline.js served from go:embed at /static/
// timeline.js) via script-src 'self'. No CDN, no 'unsafe-inline'/
// 'unsafe-eval' for scripts. style-src keeps 'unsafe-inline' for the page's
// inline <style> block; that has no code-execution implications the way an
// inline/eval script would.
const contentSecurityPolicy = "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

// setSecurityHeaders applies a conservative set of response security headers
// to every response (including error responses).
func setSecurityHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", contentSecurityPolicy)
}
