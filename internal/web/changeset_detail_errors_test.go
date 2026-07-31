package web_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// TestChangesetDetail_JSONErrorsAreJSONObjects verifies that when JSON was
// negotiated, error responses are JSON objects too — so a client parses every
// response with one code path instead of sniffing whether a body happens to be
// JSON or plain text. Status codes are unchanged from the HTML representation.
func TestChangesetDetail_JSONErrorsAreJSONObjects(t *testing.T) {
	t.Parallel()

	h, _ := seedDetailChangeset(t)

	tests := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{"missing both params", "", http.StatusBadRequest},
		{"missing commitSha", "?repo=infra-repo", http.StatusBadRequest},
		{"missing repo", "?commitSha=commit-detail", http.StatusBadRequest},
		{"unknown changeset", "?repo=infra-repo&commitSha=no-such-commit", http.StatusNotFound},
		{"unknown repo", "?repo=no-such-repo&commitSha=commit-detail", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rr := getDetail(t, h, tt.query, "application/json")
			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
			if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("error body is not a JSON object: %v; body: %s", err, rr.Body.String())
			}
			if body.Error == "" {
				t.Errorf(`error body has no "error" message: %s`, rr.Body.String())
			}
		})
	}
}

// TestChangesetDetail_HTMLErrorsUnchanged verifies the non-JSON error paths
// still behave exactly as they did before negotiation existed: same statuses,
// same plain-text bodies. Adding a representation must not perturb the one
// that already had callers.
func TestChangesetDetail_HTMLErrorsUnchanged(t *testing.T) {
	t.Parallel()

	h, _ := seedDetailChangeset(t)

	tests := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{"missing params", "", http.StatusBadRequest},
		{"unknown changeset", "?repo=infra-repo&commitSha=no-such-commit", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for _, accept := range []string{"", "*/*", "text/html"} {
				rr := getDetail(t, h, tt.query, accept)
				if rr.Code != tt.wantStatus {
					t.Errorf("Accept %q: status = %d, want %d; body: %s", accept, rr.Code, tt.wantStatus, rr.Body.String())
				}
				if strings.HasPrefix(strings.TrimSpace(rr.Body.String()), "{") {
					t.Errorf("Accept %q: error body is JSON, want plain text: %s", accept, rr.Body.String())
				}
			}
		})
	}
}

// TestChangesetDetail_ErrorsEchoNoCallerInput verifies neither representation
// reflects caller-controlled values back into an error body. The 404 path is
// the one that matters most: it is reached with caller-supplied repo and
// commitSha values, and echoing them would turn a routine not-found into a
// reflection vector.
func TestChangesetDetail_ErrorsEchoNoCallerInput(t *testing.T) {
	t.Parallel()

	h, _ := seedDetailChangeset(t)

	const hostileRepo = "evil<script>alert(1)</script>"
	const hostileSha = "sha-cafebabe-marker"
	query := "?repo=" + hostileRepo + "&commitSha=" + hostileSha

	for _, accept := range []string{"", "*/*", "application/json"} {
		rr := getDetail(t, h, query, accept)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("Accept %q: status = %d, want 404; body: %s", accept, rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		for _, echoed := range []string{"evil", "script", "alert", "cafebabe", hostileSha} {
			if strings.Contains(body, echoed) {
				t.Errorf("Accept %q: body echoes caller input %q: %s", accept, echoed, body)
			}
		}
	}
}

// TestChangesetDetail_NotFoundIsIndistinguishableAcrossRepresentations
// verifies an unknown changeset yields no extra distinguishing signal in
// either representation: a caller cannot learn whether the repo exists, the
// commit exists, or neither, and cannot learn more by switching Accept
// headers. Existence probing is the thing being prevented.
func TestChangesetDetail_NotFoundIsIndistinguishableAcrossRepresentations(t *testing.T) {
	t.Parallel()

	h, _ := seedDetailChangeset(t)

	cases := []string{
		"?repo=infra-repo&commitSha=no-such-commit",  // real repo, unknown commit
		"?repo=no-such-repo&commitSha=commit-detail", // unknown repo, real commit
		"?repo=no-such-repo&commitSha=no-such-commit",
	}

	for _, accept := range []string{"", "application/json"} {
		var first string
		for i, query := range cases {
			rr := getDetail(t, h, query, accept)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("Accept %q, %s: status = %d, want 404", accept, query, rr.Code)
			}
			if i == 0 {
				first = rr.Body.String()
				continue
			}
			if rr.Body.String() != first {
				t.Errorf("Accept %q: %s produced a different 404 body than the first case\n got: %s\nwant: %s",
					accept, query, rr.Body.String(), first)
			}
		}
	}
}

// TestChangesetDetail_SecurityHeadersOnEveryResponse verifies the security
// headers are set regardless of representation or status code. They are set
// before any branch in the handler, and this pins that: a future refactor that
// moved them into the success path would leave every error response unprotected.
func TestChangesetDetail_SecurityHeadersOnEveryResponse(t *testing.T) {
	t.Parallel()

	h, query := seedDetailChangeset(t)

	// A response known to carry the headers today, used to derive the expected
	// set rather than re-typing header names this test would then not notice
	// changing.
	baseline := getDetail(t, h, query, "")
	var headerNames []string
	for name := range baseline.Header() {
		if name == "Content-Type" || name == "Content-Length" {
			continue
		}
		headerNames = append(headerNames, name)
	}
	if len(headerNames) == 0 {
		t.Fatal("baseline response carries no security headers — this test has lost track of its subject")
	}

	requests := []struct {
		name   string
		query  string
		accept string
	}{
		{"html ok", query, ""},
		{"json ok", query, "application/json"},
		{"html bad request", "", ""},
		{"json bad request", "", "application/json"},
		{"html not found", "?repo=infra-repo&commitSha=nope", ""},
		{"json not found", "?repo=infra-repo&commitSha=nope", "application/json"},
	}

	for _, tt := range requests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rr := getDetail(t, h, tt.query, tt.accept)
			for _, name := range headerNames {
				if got := rr.Header().Get(name); got == "" {
					t.Errorf("%s response is missing security header %q", tt.name, name)
				}
			}
		})
	}
}

// TestChangesetsList_ContentTypeStillJSON guards the list endpoint against the
// writeJSON change made for this slice: writeJSON now sets Content-Type
// itself, and the list handler must still emit application/json.
func TestChangesetsList_ContentTypeStillJSON(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	rr := getChangesets(t, web.NewChangesetsHandler(st), "")
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}
