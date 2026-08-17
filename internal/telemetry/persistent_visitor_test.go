package telemetry_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
)

// TestPersistentVisitorID_MintsAndSetsCookie verifies a first-time visitor
// (no cookie) gets a fresh, well-formed UUID v4 and a Set-Cookie response
// carrying it.
func TestPersistentVisitorID_MintsAndSetsCookie(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	id, err := telemetry.PersistentVisitorID(w, r, false)
	if err != nil {
		t.Fatalf("PersistentVisitorID: %v", err)
	}
	if id == "" {
		t.Fatal("PersistentVisitorID returned an empty id")
	}

	resp := w.Result()
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != telemetry.PersistentVisitorCookie {
		t.Errorf("cookie name = %q, want %q", cookie.Name, telemetry.PersistentVisitorCookie)
	}
	if cookie.Value != id {
		t.Errorf("cookie value = %q, want %q", cookie.Value, id)
	}
	if !cookie.HttpOnly {
		t.Error("cookie is not HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.MaxAge <= 0 {
		t.Errorf("cookie MaxAge = %d, want > 0", cookie.MaxAge)
	}
}

// TestPersistentVisitorID_SecureFlag verifies the Secure flag follows the
// caller's determination rather than being hardcoded either way.
func TestPersistentVisitorID_SecureFlag(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	if _, err := telemetry.PersistentVisitorID(w, r, true); err != nil {
		t.Fatalf("PersistentVisitorID: %v", err)
	}
	if cookies := w.Result().Cookies(); len(cookies) != 1 || !cookies[0].Secure {
		t.Error("cookie was not marked Secure when secure=true")
	}
}

// TestPersistentVisitorID_ReusesExistingCookie verifies a returning visitor
// (valid cookie already present) gets the same id back and no new cookie is
// set — the whole point of the identifier is that it does not change.
func TestPersistentVisitorID_ReusesExistingCookie(t *testing.T) {
	const existing = "12345678-1234-4123-8123-123456789abc"

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: telemetry.PersistentVisitorCookie, Value: existing})
	w := httptest.NewRecorder()

	id, err := telemetry.PersistentVisitorID(w, r, false)
	if err != nil {
		t.Fatalf("PersistentVisitorID: %v", err)
	}
	if id != existing {
		t.Errorf("PersistentVisitorID = %q, want %q (the existing cookie)", id, existing)
	}
	if cookies := w.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("got %d Set-Cookie headers for an already-valid cookie, want 0", len(cookies))
	}
}

// TestPersistentVisitorID_RejectsMalformedCookie verifies a cookie value that
// isn't a well-formed UUID v4 is not trusted verbatim: a fresh id is minted
// instead, the same as if no cookie had been sent at all.
func TestPersistentVisitorID_RejectsMalformedCookie(t *testing.T) {
	tests := []string{
		"not-a-uuid",
		"",
		"12345678-1234-4123-8123-123456789abc; DROP TABLE visitors",
		"00000000-0000-0000-0000-000000000000", // valid UUID shape, but not version 4
	}

	for _, bad := range tests {
		t.Run(bad, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.AddCookie(&http.Cookie{Name: telemetry.PersistentVisitorCookie, Value: bad})
			w := httptest.NewRecorder()

			id, err := telemetry.PersistentVisitorID(w, r, false)
			if err != nil {
				t.Fatalf("PersistentVisitorID: %v", err)
			}
			if id == bad {
				t.Errorf("malformed cookie value %q was trusted verbatim", bad)
			}
			if cookies := w.Result().Cookies(); len(cookies) != 1 {
				t.Errorf("got %d Set-Cookie headers for a malformed cookie, want 1 (a replacement)", len(cookies))
			}
		})
	}
}
