package web_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// TestChangesetsAPI_EmptyStore_ReturnsEmptyJSONList verifies the tracer
// bullet: GET /api/changesets against an empty store returns 200,
// application/json, and an empty Changesets list.
func TestChangesetsAPI_EmptyStore_ReturnsEmptyJSONList(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	h := web.NewChangesetsHandler(st)

	req := httptest.NewRequest(http.MethodGet, "/api/changesets", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" && ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body struct {
		Changesets []json.RawMessage `json:"changesets"`
		NextCursor string            `json:"nextCursor"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v; body: %s", err, rr.Body.String())
	}
	if len(body.Changesets) != 0 {
		t.Errorf("Changesets len = %d, want 0", len(body.Changesets))
	}
	if body.NextCursor != "" {
		t.Errorf("NextCursor = %q, want empty", body.NextCursor)
	}
}

// changesetsBody mirrors the JSON response shape for decoding in tests.
type changesetsBody struct {
	Changesets []changesetBody `json:"changesets"`
	NextCursor string          `json:"nextCursor"`
}

type changesetBody struct {
	Repo        string       `json:"repo"`
	CommitSha   string       `json:"commitSha"`
	Author      string       `json:"author"`
	CommittedAt time.Time    `json:"committedAt"`
	Changes     []changeBody `json:"changes"`
	Risk        []string     `json:"risk"`
}

type changeBody struct {
	Field      string  `json:"field"`
	Key        *string `json:"key,omitempty"`
	ChangeType string  `json:"changeType"`
	OldValue   *string `json:"oldValue,omitempty"`
	NewValue   *string `json:"newValue,omitempty"`
	Kind       string  `json:"kind"`
}

// decodeChangesetsBody unmarshals a recorded response body into
// changesetsBody, failing the test on error.
func decodeChangesetsBody(t *testing.T, rr *httptest.ResponseRecorder) changesetsBody {
	t.Helper()
	var body changesetsBody
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v; body: %s", err, rr.Body.String())
	}
	return body
}

// TestChangesetsAPI_AsOfAbsent_DefaultsToNow verifies that when asOf is
// omitted, the endpoint defaults to "now" — a Change committed in the past
// is returned without requiring the caller to pass asOf explicitly.
func TestChangesetsAPI_AsOfAbsent_DefaultsToNow(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	past := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		CommitSha: "commit-past", Author: "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(past); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetsHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := decodeChangesetsBody(t, rr)
	if len(body.Changesets) != 1 {
		t.Fatalf("Changesets len = %d, want 1 (asOf should default to now)", len(body.Changesets))
	}
	if body.Changesets[0].CommitSha != "commit-past" {
		t.Errorf("CommitSha = %q, want commit-past", body.Changesets[0].CommitSha)
	}
}

// TestChangesetsAPI_IncludeFacetFilter verifies that a tri-state include
// facet param (e.g. tier=dev) reaches the store query and filters the
// returned Changesets to only those matching.
func TestChangesetsAPI_IncludeFacetFilter(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	base := time.Now().Add(-2 * time.Hour)

	dev := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"tier": "dev"},
		CommitSha:   "commit-dev",
		Author:      "alice",
		CommittedAt: base,
	}
	prod := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"tier": "prod"},
		CommitSha:   "commit-prod",
		Author:      "bob",
		CommittedAt: base.Add(time.Hour),
	}
	if err := st.SaveChange(dev); err != nil {
		t.Fatalf("SaveChange dev: %v", err)
	}
	if err := st.SaveChange(prod); err != nil {
		t.Fatalf("SaveChange prod: %v", err)
	}

	h := web.NewChangesetsHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets?tier=dev", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := decodeChangesetsBody(t, rr)
	if len(body.Changesets) != 1 {
		t.Fatalf("Changesets len = %d, want 1 (tier=dev only)", len(body.Changesets))
	}
	if body.Changesets[0].CommitSha != "commit-dev" {
		t.Errorf("CommitSha = %q, want commit-dev", body.Changesets[0].CommitSha)
	}
}

// TestChangesetsAPI_ExcludeFacetFilter_AbsentSurvives verifies the tri-state
// exclude semantic end-to-end through the HTTP layer: tier=-sbx excludes an
// explicit tier=sbx match but a Changeset with no tier facet at all survives.
func TestChangesetsAPI_ExcludeFacetFilter_AbsentSurvives(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	base := time.Now().Add(-2 * time.Hour)

	sbx := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"tier": "sbx"},
		CommitSha:   "commit-sbx",
		Author:      "alice",
		CommittedAt: base,
	}
	noTier := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"env": "dev"},
		CommitSha:   "commit-no-tier",
		Author:      "bob",
		CommittedAt: base.Add(time.Hour),
	}
	if err := st.SaveChange(sbx); err != nil {
		t.Fatalf("SaveChange sbx: %v", err)
	}
	if err := st.SaveChange(noTier); err != nil {
		t.Fatalf("SaveChange noTier: %v", err)
	}

	h := web.NewChangesetsHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets?tier=-sbx", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := decodeChangesetsBody(t, rr)
	if len(body.Changesets) != 1 {
		t.Fatalf("Changesets len = %d, want 1 (sbx excluded; facet-absent survives)", len(body.Changesets))
	}
	if body.Changesets[0].CommitSha != "commit-no-tier" {
		t.Errorf("CommitSha = %q, want commit-no-tier", body.Changesets[0].CommitSha)
	}
}

// TestChangesetsAPI_CombinedIncludeAndExcludeFilter verifies include-AND ∧
// exclude-none semantics reach the store via the HTTP layer.
func TestChangesetsAPI_CombinedIncludeAndExcludeFilter(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	base := time.Now().Add(-3 * time.Hour)

	devSbx := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"env": "dev", "tier": "sbx"},
		CommitSha:   "commit-dev-sbx",
		Author:      "alice",
		CommittedAt: base,
	}
	devNoTier := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"env": "dev"},
		CommitSha:   "commit-dev-no-tier",
		Author:      "bob",
		CommittedAt: base.Add(time.Hour),
	}
	prodNoTier := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"env": "prod"},
		CommitSha:   "commit-prod-no-tier",
		Author:      "carol",
		CommittedAt: base.Add(2 * time.Hour),
	}
	for _, c := range []domain.Change{devSbx, devNoTier, prodNoTier} {
		if err := st.SaveChange(c); err != nil {
			t.Fatalf("SaveChange: %v", err)
		}
	}

	h := web.NewChangesetsHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets?env=dev&tier=-sbx", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := decodeChangesetsBody(t, rr)
	if len(body.Changesets) != 1 {
		t.Fatalf("Changesets len = %d, want 1 (env=dev AND NOT tier=sbx)", len(body.Changesets))
	}
	if body.Changesets[0].CommitSha != "commit-dev-no-tier" {
		t.Errorf("CommitSha = %q, want commit-dev-no-tier", body.Changesets[0].CommitSha)
	}
}

// TestChangesetsAPI_RepoFilter_ScopesToOneRepo verifies R26: a ?repo= query
// param scopes the returned Changesets to that one tracked repository.
func TestChangesetsAPI_RepoFilter_ScopesToOneRepo(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	base := time.Now().Add(-2 * time.Hour)

	apps := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		CommitSha: "commit-apps", Author: "alice", CommittedAt: base,
	}
	infra := domain.Change{
		Repo: "infra-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		CommitSha: "commit-infra", Author: "bob", CommittedAt: base.Add(time.Hour),
	}
	if err := st.SaveChange(apps); err != nil {
		t.Fatalf("SaveChange apps: %v", err)
	}
	if err := st.SaveChange(infra); err != nil {
		t.Fatalf("SaveChange infra: %v", err)
	}

	h := web.NewChangesetsHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets?repo=apps-repo", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := decodeChangesetsBody(t, rr)
	if len(body.Changesets) != 1 {
		t.Fatalf("Changesets len = %d, want 1 (repo=apps-repo only)", len(body.Changesets))
	}
	if body.Changesets[0].CommitSha != "commit-apps" {
		t.Errorf("CommitSha = %q, want commit-apps", body.Changesets[0].CommitSha)
	}
}

// TestChangesetsAPI_RepoFilter_ComposesWithFacetFilterAND verifies R27: the
// repo filter composes with a tri-state facet filter via AND — a Changeset
// satisfying only one of the two must not appear.
func TestChangesetsAPI_RepoFilter_ComposesWithFacetFilterAND(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	base := time.Now().Add(-3 * time.Hour)

	appsDev := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"env": "dev"},
		CommitSha:   "commit-apps-dev",
		Author:      "alice",
		CommittedAt: base,
	}
	appsProd := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"env": "prod"},
		CommitSha:   "commit-apps-prod",
		Author:      "bob",
		CommittedAt: base.Add(time.Hour),
	}
	infraDev := domain.Change{
		Repo: "infra-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"env": "dev"},
		CommitSha:   "commit-infra-dev",
		Author:      "carol",
		CommittedAt: base.Add(2 * time.Hour),
	}
	for _, c := range []domain.Change{appsDev, appsProd, infraDev} {
		if err := st.SaveChange(c); err != nil {
			t.Fatalf("SaveChange: %v", err)
		}
	}

	h := web.NewChangesetsHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets?repo=apps-repo&env=dev", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := decodeChangesetsBody(t, rr)
	if len(body.Changesets) != 1 {
		t.Fatalf("Changesets len = %d, want 1 (repo=apps-repo AND env=dev)", len(body.Changesets))
	}
	if body.Changesets[0].CommitSha != "commit-apps-dev" {
		t.Errorf("CommitSha = %q, want commit-apps-dev", body.Changesets[0].CommitSha)
	}
}

// TestChangesetsAPI_RepoParamNotTreatedAsFacet verifies that "repo" is a
// reserved param — even if a stored Change happens to carry a facet
// literally named "repo", it is never matched as a tri-state facet filter,
// only as the distinguished repo scope.
func TestChangesetsAPI_RepoParamNotTreatedAsFacet(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	base := time.Now().Add(-time.Hour)

	c := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"repo": "some-facet-value"},
		CommitSha:   "commit-with-repo-facet",
		Author:      "alice",
		CommittedAt: base,
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetsHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets?repo=apps-repo", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := decodeChangesetsBody(t, rr)
	if len(body.Changesets) != 1 {
		t.Fatalf("Changesets len = %d, want 1 (repo param must scope by Change.Repo, not the \"repo\" facet)", len(body.Changesets))
	}
	if body.Changesets[0].CommitSha != "commit-with-repo-facet" {
		t.Errorf("CommitSha = %q, want commit-with-repo-facet", body.Changesets[0].CommitSha)
	}
}

// TestChangesetsAPI_RepoAbsent_MatchesEveryRepo verifies that omitting the
// repo param is a no-op — Changesets from every repo are returned, matching
// FilterSpec's empty-scope invariant.
func TestChangesetsAPI_RepoAbsent_MatchesEveryRepo(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	base := time.Now().Add(-2 * time.Hour)

	apps := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		CommitSha: "commit-apps", Author: "alice", CommittedAt: base,
	}
	infra := domain.Change{
		Repo: "infra-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		CommitSha: "commit-infra", Author: "bob", CommittedAt: base.Add(time.Hour),
	}
	if err := st.SaveChange(apps); err != nil {
		t.Fatalf("SaveChange apps: %v", err)
	}
	if err := st.SaveChange(infra); err != nil {
		t.Fatalf("SaveChange infra: %v", err)
	}

	h := web.NewChangesetsHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := decodeChangesetsBody(t, rr)
	if len(body.Changesets) != 2 {
		t.Fatalf("Changesets len = %d, want 2 (repo absent matches every repo)", len(body.Changesets))
	}
}

// TestChangesetsAPI_ReservedParamsAreNotTreatedAsFacets verifies that asOf,
// cursor, and limit are never interpreted as facet filters — even if a
// stored Change happens to carry a facet with one of those exact names, the
// reserved param must control paging/time-bounding, not filtering.
func TestChangesetsAPI_ReservedParamsAreNotTreatedAsFacets(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	base := time.Now().Add(-time.Hour)

	// A Change that carries a facet literally named "cursor" with a value
	// that would never match an opaque cursor string. If "cursor" were
	// (wrongly) treated as a facet filter, this Changeset would vanish from
	// results because the include filter for "cursor" would never match.
	c := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"cursor": "some-value"},
		CommitSha:   "commit-with-cursor-facet",
		Author:      "alice",
		CommittedAt: base,
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetsHandler(st)
	// cursor="" (empty/first page) must not be misread as a facet filter
	// value that excludes the "cursor" facet's Changeset.
	req := httptest.NewRequest(http.MethodGet, "/api/changesets?cursor=", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := decodeChangesetsBody(t, rr)
	if len(body.Changesets) != 1 {
		t.Fatalf("Changesets len = %d, want 1 (cursor param must not be treated as a facet filter)", len(body.Changesets))
	}
	if body.Changesets[0].CommitSha != "commit-with-cursor-facet" {
		t.Errorf("CommitSha = %q, want commit-with-cursor-facet", body.Changesets[0].CommitSha)
	}
}

// TestChangesetsAPI_Pagination_WalksFullSetWithNoGapsOrOverlaps verifies
// that following NextCursor page after page returns every Changeset exactly
// once, most-recent-first, and the last page carries an empty NextCursor.
func TestChangesetsAPI_Pagination_WalksFullSetWithNoGapsOrOverlaps(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	base := time.Now().Add(-24 * time.Hour)

	const totalCommits = 7
	for i := 0; i < totalCommits; i++ {
		c := domain.Change{
			Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
			ChangeType:  domain.ChangeTypeModified,
			OldValue:    ptr("a"),
			NewValue:    ptr("b"),
			CommitSha:   fmt.Sprintf("commit-%02d", i),
			Author:      "alice",
			CommittedAt: base.Add(time.Duration(i) * time.Hour),
		}
		if err := st.SaveChange(c); err != nil {
			t.Fatalf("SaveChange: %v", err)
		}
	}

	h := web.NewChangesetsHandler(st)

	var gotShas []string
	cursor := ""
	for pages := 0; pages < totalCommits+1; pages++ { // hard cap to avoid infinite loop on a bug
		u := "/api/changesets?limit=3"
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}
		req := httptest.NewRequest(http.MethodGet, u, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("page %d: status = %d, want 200; body: %s", pages, rr.Code, rr.Body.String())
		}
		body := decodeChangesetsBody(t, rr)
		for _, cs := range body.Changesets {
			gotShas = append(gotShas, cs.CommitSha)
		}
		if body.NextCursor == "" {
			break
		}
		cursor = body.NextCursor
	}

	if len(gotShas) != totalCommits {
		t.Fatalf("walked %d Changesets across all pages, want %d (no gaps/overlaps): %v", len(gotShas), totalCommits, gotShas)
	}

	wantShas := make([]string, totalCommits)
	for i := 0; i < totalCommits; i++ {
		wantShas[i] = fmt.Sprintf("commit-%02d", totalCommits-1-i)
	}
	for i, want := range wantShas {
		if gotShas[i] != want {
			t.Errorf("gotShas[%d] = %q, want %q (full order: %v)", i, gotShas[i], want, gotShas)
			break
		}
	}
}

// TestChangesetsAPI_LimitClampedToServerCap verifies that a caller-supplied
// limit larger than the server-side hard cap is clamped, not passed through
// unbounded to the store. Closes the deferred MEDIUM (unbounded row fetch)
// from the store-changeset-query slice.
func TestChangesetsAPI_LimitClampedToServerCap(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	base := time.Now().Add(-24 * time.Hour)

	// Seed more commits than the server-side cap so an unclamped limit would
	// return them all in one page (no NextCursor); a clamped limit must
	// still page.
	const totalCommits = 150
	for i := 0; i < totalCommits; i++ {
		c := domain.Change{
			Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
			ChangeType:  domain.ChangeTypeModified,
			OldValue:    ptr("a"),
			NewValue:    ptr("b"),
			CommitSha:   fmt.Sprintf("commit-%03d", i),
			Author:      "alice",
			CommittedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := st.SaveChange(c); err != nil {
			t.Fatalf("SaveChange: %v", err)
		}
	}

	h := web.NewChangesetsHandler(st)
	// Request an outlandishly large limit — must be clamped server-side.
	req := httptest.NewRequest(http.MethodGet, "/api/changesets?limit=100000", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := decodeChangesetsBody(t, rr)
	if len(body.Changesets) >= totalCommits {
		t.Fatalf("Changesets len = %d, want fewer than %d seeded (limit must be clamped to a server-side cap)", len(body.Changesets), totalCommits)
	}
	if len(body.Changesets) == 0 {
		t.Fatal("Changesets len = 0, want a clamped-but-nonzero page")
	}
	if body.NextCursor == "" {
		t.Error("NextCursor is empty, want non-empty (a clamped page over 150 seeded commits must not be the last page)")
	}

	// The clamp must be a hard cap, not merely "less than 150": requesting
	// limit=100000 must not yield more Changesets than a small, explicit
	// limit request would allow through the same cap. Concretely: the page
	// must be strictly smaller than totalCommits by a wide margin, proving
	// the requested 100000 never reached the store unclamped.
	if len(body.Changesets) > 100 {
		t.Errorf("Changesets len = %d, requested limit=100000 was not clamped to a reasonably small server-side cap", len(body.Changesets))
	}
}

// TestChangesetsAPI_LimitRespectsExplicitSmallerValue verifies that when the
// caller requests a limit smaller than the server-side cap, that smaller
// value is honored (not silently replaced by the default or the cap).
func TestChangesetsAPI_LimitRespectsExplicitSmallerValue(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	base := time.Now().Add(-24 * time.Hour)

	const totalCommits = 5
	for i := 0; i < totalCommits; i++ {
		c := domain.Change{
			Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
			ChangeType:  domain.ChangeTypeModified,
			OldValue:    ptr("a"),
			NewValue:    ptr("b"),
			CommitSha:   fmt.Sprintf("commit-%02d", i),
			Author:      "alice",
			CommittedAt: base.Add(time.Duration(i) * time.Hour),
		}
		if err := st.SaveChange(c); err != nil {
			t.Fatalf("SaveChange: %v", err)
		}
	}

	h := web.NewChangesetsHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets?limit=2", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := decodeChangesetsBody(t, rr)
	if len(body.Changesets) != 2 {
		t.Fatalf("Changesets len = %d, want 2 (explicit limit=2 honored)", len(body.Changesets))
	}
	if body.NextCursor == "" {
		t.Error("NextCursor is empty, want non-empty (5 seeded commits, limit=2 means more pages remain)")
	}
}

// TestChangesetsAPI_ResponseShape_ChartAndValueKindsAndOrdering verifies the
// full response shape: commit metadata (repo, commitSha, author,
// committedAt), each Change's field/key/changeType/old-new value, the
// chart-vs-value kind classification, and most-recent-first ordering — all
// through the JSON wire format.
func TestChangesetsAPI_ResponseShape_ChartAndValueKindsAndOrdering(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	base := time.Now().Add(-3 * time.Hour)

	keyVal := "kanpai-gateway"
	chartChange := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-zero/dev/us-west-2/Chart.yaml",
		Field:       "subchart-versions",
		Key:         &keyVal,
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("0.38.0"),
		NewValue:    ptr("0.39.0"),
		CommitSha:   "commit-older",
		Author:      "alice",
		CommittedAt: base,
	}
	valueChange := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-zero/dev/us-west-2/values.yaml",
		Field:       "image-tag",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("v1"),
		NewValue:    ptr("v2"),
		CommitSha:   "commit-newer",
		Author:      "bob",
		CommittedAt: base.Add(time.Hour),
	}
	if err := st.SaveChange(chartChange); err != nil {
		t.Fatalf("SaveChange chart: %v", err)
	}
	if err := st.SaveChange(valueChange); err != nil {
		t.Fatalf("SaveChange value: %v", err)
	}

	h := web.NewChangesetsHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := decodeChangesetsBody(t, rr)
	if len(body.Changesets) != 2 {
		t.Fatalf("Changesets len = %d, want 2", len(body.Changesets))
	}

	// Most-recent-first: commit-newer before commit-older.
	if body.Changesets[0].CommitSha != "commit-newer" {
		t.Errorf("Changesets[0].CommitSha = %q, want commit-newer (most-recent-first)", body.Changesets[0].CommitSha)
	}
	if body.Changesets[1].CommitSha != "commit-older" {
		t.Errorf("Changesets[1].CommitSha = %q, want commit-older", body.Changesets[1].CommitSha)
	}

	newer := body.Changesets[0]
	if newer.Repo != "apps-repo" {
		t.Errorf("Changesets[0].Repo = %q, want apps-repo", newer.Repo)
	}
	if newer.Author != "bob" {
		t.Errorf("Changesets[0].Author = %q, want bob", newer.Author)
	}
	if len(newer.Changes) != 1 {
		t.Fatalf("Changesets[0].Changes len = %d, want 1", len(newer.Changes))
	}
	valueCh := newer.Changes[0]
	if valueCh.Field != "image-tag" {
		t.Errorf("value Change.Field = %q, want image-tag", valueCh.Field)
	}
	if valueCh.ChangeType != "modified" {
		t.Errorf("value Change.ChangeType = %q, want modified", valueCh.ChangeType)
	}
	if valueCh.OldValue == nil || *valueCh.OldValue != "v1" {
		t.Errorf("value Change.OldValue = %v, want v1", valueCh.OldValue)
	}
	if valueCh.NewValue == nil || *valueCh.NewValue != "v2" {
		t.Errorf("value Change.NewValue = %v, want v2", valueCh.NewValue)
	}
	if valueCh.Kind != "value" {
		t.Errorf("value Change.Kind = %q, want value", valueCh.Kind)
	}

	older := body.Changesets[1]
	if len(older.Changes) != 1 {
		t.Fatalf("Changesets[1].Changes len = %d, want 1", len(older.Changes))
	}
	chartCh := older.Changes[0]
	if chartCh.Field != "subchart-versions" {
		t.Errorf("chart Change.Field = %q, want subchart-versions", chartCh.Field)
	}
	if chartCh.Key == nil || *chartCh.Key != "kanpai-gateway" {
		t.Errorf("chart Change.Key = %v, want kanpai-gateway", chartCh.Key)
	}
	if chartCh.Kind != "chart" {
		t.Errorf("chart Change.Kind = %q, want chart", chartCh.Kind)
	}
}

// TestChangesetsAPI_MalformedAsOf_Returns400Generic verifies that an asOf
// value that fails RFC3339 parsing is rejected with a generic 400 — the
// malformed input itself must never be echoed back in the response body.
func TestChangesetsAPI_MalformedAsOf_Returns400Generic(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	h := web.NewChangesetsHandler(st)

	const badAsOf = "not-a-timestamp<script>"
	req := httptest.NewRequest(http.MethodGet, "/api/changesets?asOf="+url.QueryEscape(badAsOf), nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, badAsOf) || strings.Contains(body, "not-a-timestamp") {
		t.Errorf("error body echoes caller input: %q", body)
	}
}

// TestChangesetsAPI_MalformedCursor_Returns400Generic verifies that a cursor
// that fails to decode is rejected with a generic 400 — the opaque cursor
// bytes must never be echoed back.
func TestChangesetsAPI_MalformedCursor_Returns400Generic(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	h := web.NewChangesetsHandler(st)

	const badCursor = "not-a-valid-cursor!!!"
	req := httptest.NewRequest(http.MethodGet, "/api/changesets?cursor="+url.QueryEscape(badCursor), nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, badCursor) {
		t.Errorf("error body echoes caller input: %q", body)
	}
}

// TestChangesetsAPI_InvalidFacetKey_Returns400Generic verifies that a query
// param name that is not a known facet (and therefore rejected by
// filter.Parse's allowlist) never actually reaches that rejection path here
// — the endpoint's own allowlist behavior silently ignores unknown params
// (matching the HTML handler's convention), so an "unknown" facet key alone
// must not error. This test instead proves the malformed-cursor/asOf error
// responses share the same generic message and never leak internals such as
// SQL text or facet key names.
func TestChangesetsAPI_MalformedInput_ErrorBodyIsGenericAndConsistent(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	h := web.NewChangesetsHandler(st)

	cases := []struct {
		name string
		url  string
	}{
		{"bad asOf", "/api/changesets?asOf=garbage"},
		{"bad cursor", "/api/changesets?cursor=garbage!!"},
		{"bad limit", "/api/changesets?limit=not-a-number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
			}
			body := strings.TrimSpace(rr.Body.String())
			if body != "bad request" {
				t.Errorf("body = %q, want generic %q", body, "bad request")
			}
		})
	}
}

// TestChangesetsAPI_StoreFailure_Returns500Generic verifies that a store
// failure (e.g. a closed database) surfaces as a generic 500 with no
// internal detail (DB file path, SQL text) leaked to the client.
func TestChangesetsAPI_StoreFailure_Returns500Generic(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	h := web.NewChangesetsHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "internal server error") {
		t.Errorf("body = %q, want generic 'internal server error'", body)
	}
	if strings.Contains(body, ".db") || strings.Contains(strings.ToLower(body), "sql") {
		t.Errorf("error body leaks internal detail: %q", body)
	}
}

// TestChangesetsAPI_UnknownFacetParam_SilentlyIgnored verifies that a query
// param that is not a recognized facet name (no stored Change carries it) is
// silently ignored rather than rejected — matching the HTML feed handler's
// existing whitelist convention. This also means a typo'd or unrelated query
// param never causes a 400 by itself.
func TestChangesetsAPI_UnknownFacetParam_SilentlyIgnored(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	base := time.Now().Add(-time.Hour)

	c := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"env": "dev"},
		CommitSha:   "commit-x",
		Author:      "alice",
		CommittedAt: base,
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetsHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets?nonexistentFacet=whatever", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (unknown facet param must be ignored, not rejected); body: %s", rr.Code, rr.Body.String())
	}
	body := decodeChangesetsBody(t, rr)
	if len(body.Changesets) != 1 {
		t.Fatalf("Changesets len = %d, want 1", len(body.Changesets))
	}
}

// TestChangesetsAPI_SecurityHeaders verifies the JSON endpoint carries the
// same conservative security headers as the HTML feed handler — the
// security posture must stay intact across both response surfaces.
func TestChangesetsAPI_SecurityHeaders(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	h := web.NewChangesetsHandler(st)

	req := httptest.NewRequest(http.MethodGet, "/api/changesets", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	}
	for k, v := range want {
		if got := rr.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
}
