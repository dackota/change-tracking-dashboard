package web_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// logRecord is one JSON log line, decoded far enough to assert on.
type logRecord struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Risk    string `json:"risk"`
	Remedy  string `json:"remedy"`
}

// serveChangesetsCapturingLogs issues a GET against a changesets handler with
// a logger whose output the test can read back.
func serveChangesetsCapturingLogs(t *testing.T, h http.Handler, query string) (*httptest.ResponseRecorder, []logRecord) {
	t.Helper()

	// telemetry.NewLogger, not a bare slog handler: it applies the service's
	// log contract (the "message"/"timestamp" key remapping), so these tests
	// assert against the shape an operator actually greps.
	var buf bytes.Buffer
	logger := telemetry.NewLogger("change-tracking-dashboard", &buf)

	req := httptest.NewRequest(http.MethodGet, "/api/changesets"+query, nil)
	req = req.WithContext(telemetry.ContextWithLogger(req.Context(), logger))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	var records []logRecord
	scanner := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec logRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %v; line: %s", err, line)
		}
		records = append(records, rec)
	}
	return rr, records
}

// seedOneChangeset stores a single ordinary changeset, so a filtered query has
// something to not match rather than trivially returning nothing.
func seedOneChangeset(t *testing.T) *web.ChangesetsHandler {
	t.Helper()

	st := newTestStore(t)
	c := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "workloads/app/values.yaml",
		Field:       "imageTags",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("1.9.0"),
		NewValue:    ptr("2.0.0"),
		CommitSha:   "commit-reachability",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}
	return web.NewChangesetsHandler(st)
}

// findRecord returns the first record whose Message contains substr.
func findRecord(records []logRecord, substr string) (logRecord, bool) {
	for _, rec := range records {
		if strings.Contains(rec.Message, substr) {
			return rec, true
		}
	}
	return logRecord{}, false
}

// TestChangesets_RiskFilterNamingUnreachableClass_WarnsWithRemedy is the fix
// for #157's real defect.
//
// risk=major-version-bump is a valid slug, so it is accepted — and on a
// zero-config deployment no rule can ever produce that class, so the response
// is an empty list. To a consumer that reads as "no breaking upgrades in this
// window" when it actually means "this class is not configured here". The two
// are indistinguishable from the response, and the second is a configuration
// problem the operator can fix.
//
// The signal therefore goes to the operator, in the logs, naming the class and
// the remedy. It is a warning rather than an error: the request is valid and
// the empty result is a truthful answer to what was asked.
func TestChangesets_RiskFilterNamingUnreachableClass_WarnsWithRemedy(t *testing.T) {
	t.Parallel()

	h := seedOneChangeset(t)

	rr, records := serveChangesetsCapturingLogs(t, h, "?risk=major-version-bump")

	// The request stays valid: a slug in the closed vocabulary is not a
	// client error just because this deployment has no rule for it.
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	rec, ok := findRecord(records, "risk filter")
	if !ok {
		t.Fatalf("no log record mentions the risk filter; records: %+v", records)
	}
	if rec.Level != "WARN" {
		t.Errorf("level = %q, want WARN — the request is valid, so this is not an error", rec.Level)
	}
	if rec.Risk != "major-version-bump" {
		t.Errorf("record does not name the unreachable class: risk = %q, want major-version-bump", rec.Risk)
	}
	// An operator reading this must be able to act on it without going to
	// git history, so the remedy travels with the warning.
	if !strings.Contains(rec.Remedy, "semverBump") && !strings.Contains(rec.Remedy, "riskRules") {
		t.Errorf("remedy = %q, want it to name the configuration that would make the class reachable", rec.Remedy)
	}
}

// TestChangesets_RiskFilterNamingReachableClass_DoesNotWarn keeps the warning
// meaningful. A filter naming a class the active rules DO produce is ordinary
// use — an empty result there really does mean "nothing matched" — so it must
// stay quiet. A warning that fires on the common path is one operators learn
// to ignore.
func TestChangesets_RiskFilterNamingReachableClass_DoesNotWarn(t *testing.T) {
	t.Parallel()

	h := seedOneChangeset(t)

	for _, slug := range []string{"security", "cost-tripwire", "replace-destroy"} {
		t.Run(slug, func(t *testing.T) {
			t.Parallel()

			_, records := serveChangesetsCapturingLogs(t, h, "?risk="+slug)
			if rec, ok := findRecord(records, "risk filter"); ok && rec.Level == "WARN" {
				t.Errorf("slug %q is produced by the default rules but still warned: %+v", slug, rec)
			}
		})
	}
}

// TestChangesets_NoRiskFilter_DoesNotWarn verifies the check is scoped to what
// the caller actually asked for. An unfiltered request says nothing about
// major-version-bump and must not be warned about it.
func TestChangesets_NoRiskFilter_DoesNotWarn(t *testing.T) {
	t.Parallel()

	h := seedOneChangeset(t)

	_, records := serveChangesetsCapturingLogs(t, h, "")
	if rec, ok := findRecord(records, "risk filter"); ok && rec.Level == "WARN" {
		t.Errorf("an unfiltered request warned about the risk filter: %+v", rec)
	}
}

// TestChangesets_ConfiguredRuleMakesClassReachable_NoWarning verifies the
// check consults the ACTIVE rule set, not the shipped defaults. An operator
// who followed the README's recipe has made the class reachable, and warning
// them anyway would be false and would teach them to ignore the message.
func TestChangesets_ConfiguredRuleMakesClassReachable_NoWarning(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	c := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "workloads/app/values.yaml",
		Field:       "imageTags",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("1.9.0"),
		NewValue:    ptr("2.0.0"),
		CommitSha:   "commit-configured",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	configured := append(changeset.DefaultRiskRules(), changeset.RiskRule{
		Name:       "semver-major-bump",
		Risk:       changeset.RiskMajorVersionBump,
		SemverBump: changeset.SemverBumpMajor,
	})
	h := web.NewChangesetsHandler(st, web.WithChangesetsRiskRules(fakeRiskSource{rules: configured}))

	rr, records := serveChangesetsCapturingLogs(t, h, "?risk=major-version-bump")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if rec, ok := findRecord(records, "risk filter"); ok && rec.Level == "WARN" {
		t.Errorf("warned despite the operator having configured a rule for the class: %+v", rec)
	}

	// And the configured rule really does make the class reachable, so the
	// no-warning assertion above is not passing vacuously.
	var body struct {
		Changesets []struct {
			Risk []string `json:"risk"`
		} `json:"changesets"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if len(body.Changesets) != 1 {
		t.Fatalf("got %d changesets, want 1 — the configured rule should match the seeded 1.9.0 -> 2.0.0 change; body: %s",
			len(body.Changesets), rr.Body.String())
	}
}
