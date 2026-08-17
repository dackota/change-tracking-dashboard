package telemetry

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"regexp"
	"time"
)

// PersistentVisitorCookie names the first-party cookie the stable visitor ID
// is stored in.
const PersistentVisitorCookie = "visitor_id"

// persistentVisitorMaxAge is how long the cookie lives. Long enough that a
// visitor who returns weeks or months later is still recognized as the same
// browser, which is the entire point of this identifier — VisitorID already
// covers same-day uniques and rotates by design.
const persistentVisitorMaxAge = 365 * 24 * time.Hour

// uuidV4Pattern matches a well-formed RFC 4122 version-4 UUID. A cookie value
// that doesn't match is treated as absent rather than trusted verbatim: the
// value is client-supplied, and a browser (or a curious visitor in devtools)
// could otherwise hand back an arbitrary string.
var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// newVisitorUUID returns a random RFC 4122 version-4 UUID, drawn from
// crypto/rand so it cannot be predicted or enumerated. It carries no
// information about the client — it is not derived from the address,
// User-Agent, or anything else — so it cannot be reversed to an identity, and
// two browsers are never assigned the same one by construction.
func newVisitorUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate visitor id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// PersistentVisitorID returns the stable identifier for r's browser: the
// value of the visitor_id cookie when present and well-formed, or a freshly
// minted one otherwise, which it sets on w before returning.
//
// This is a different metric than VisitorID. VisitorID rotates every
// midnight UTC by design, so it can only ever answer "how many distinct
// visitors today". This identifier does not rotate — it is designed to
// answer "has this browser been here before" and "how many times" — at the
// cost of being genuine long-term state stored in the visitor's browser,
// which VisitorID deliberately avoids. A deployment should prefer VisitorID
// unless it specifically needs return-visit tracking.
//
// secure controls the cookie's Secure flag. Pass true only when the request
// is known to be HTTPS (directly or via a trusted proxy's
// X-Forwarded-Proto), since a Secure cookie set over plaintext HTTP is
// silently dropped by the browser.
func PersistentVisitorID(w http.ResponseWriter, r *http.Request, secure bool) (string, error) {
	if cookie, err := r.Cookie(PersistentVisitorCookie); err == nil && uuidV4Pattern.MatchString(cookie.Value) {
		return cookie.Value, nil
	}

	id, err := newVisitorUUID()
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     PersistentVisitorCookie,
		Value:    id,
		Path:     "/",
		MaxAge:   int(persistentVisitorMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	return id, nil
}
