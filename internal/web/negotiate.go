// Package web (this file): Accept-header content negotiation. This is the
// single place that decides "JSON or HTML?" for every endpoint that serves
// both representations, so the rule cannot drift between them as more
// endpoints learn to speak JSON.
package web

import (
	"net/http"
	"strings"
)

// jsonMediaType is the one media type that opts a request into JSON.
const jsonMediaType = "application/json"

// wantsJSON reports whether an Accept header explicitly names
// application/json.
//
// The rule is deliberately strict: only an exact mention counts. A wildcard —
// "*/*" or "application/*" — does NOT opt in, and neither does an absent
// header.
//
// That strictness is load-bearing rather than pedantic. The dashboard's own
// timeline.js fetches these endpoints with XMLHttpRequest without setting an
// Accept header, so the browser sends "*/*". If a wildcard counted as opting
// in, the live UI would start receiving JSON where it expects HTML fragments
// to splice into the page — a break that no API-level test would catch,
// because from the API's point of view everything would look correct. HTML is
// therefore the default for anything that does not ask for JSON by name.
//
// Media-type parameters (";q=0.9") are ignored: this is a presence check, not
// a quality-value negotiation. Ranking by q-value would add real complexity to
// serve a case no caller has — a client that wants JSON says so, and one that
// expresses a subtle preference between the two is better served by getting
// the explicit-mention behavior it can predict.
func wantsJSON(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		// Drop any parameters (";q=0.9", ";charset=...") and normalize.
		mediaType := part
		if i := strings.IndexByte(mediaType, ';'); i >= 0 {
			mediaType = mediaType[:i]
		}
		if strings.EqualFold(strings.TrimSpace(mediaType), jsonMediaType) {
			return true
		}
	}
	return false
}

// writeDiffNotFound responds 404 in whichever representation was negotiated,
// for the two diff endpoints (chart-diff, plan-diff).
//
// It exists alongside writeDetailError rather than reusing it because the diff
// endpoints' HTML 404 has always been http.NotFound's body ("404 page not
// found"), not the detail endpoint's generic message. Adding a JSON
// representation must not disturb what a non-JSON caller sees, so the HTML
// branch keeps calling http.NotFound and stays byte-for-byte what it was.
//
// Both of the endpoints' 404 causes — an unknown changeset, and a known
// changeset whose Changes contain no matching path — route through this one
// function in both representations, so the two remain indistinguishable and a
// caller cannot enumerate ingested commits by switching Accept headers.
func writeDiffNotFound(r *http.Request, w http.ResponseWriter, asJSON bool) {
	if asJSON {
		writeJSON(r, w, http.StatusNotFound, errorJSON{Error: genericNotFoundMsg})
		return
	}
	http.NotFound(w, r)
}
