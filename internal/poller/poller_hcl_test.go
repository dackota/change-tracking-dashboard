// poller_hcl_test.go proves the HCL extraction backend end-to-end through
// the same seam every other engine is proven through — Poller.Poll against a
// fixture git repo (S1 in the PRD's testing decisions) — never the private
// hclextract traversal internals.
package poller_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/poller"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// writeAndCommitTF writes relPath (creating parent dirs) and commits it,
// returning the new commit sha.
func writeAndCommitTF(t *testing.T, repoPath, relPath, content, msg string, when time.Time) string {
	t.Helper()

	full := filepath.Join(repoPath, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %q: %v", relPath, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", relPath, err)
	}

	r, err := git.PlainOpen(repoPath)
	if err != nil {
		t.Fatalf("git open: %v", err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add(relPath); err != nil {
		t.Fatalf("git add %q: %v", relPath, err)
	}
	h, err := wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{Name: "dev", Email: "d@x.com", When: when},
	})
	if err != nil {
		t.Fatalf("commit %q: %v", relPath, err)
	}
	return h.String()
}

// initTFRepo creates an empty git repo ready for writeAndCommitTF calls.
func initTFRepo(t *testing.T) (repoPath string) {
	t.Helper()
	dir := t.TempDir()
	if _, err := git.PlainInit(dir, false); err != nil {
		t.Fatalf("git init: %v", err)
	}
	return dir
}

func versionsTFContent(providerVersion, requiredVersion string) string {
	return `terraform {
  required_version = "` + requiredVersion + `"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "` + providerVersion + `"
    }
  }
}
`
}

var tfBase = time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

// TestPoller_HCL_AutoDetectsEngineFromGlob_AndTracksProviderVersion proves
// acceptance criteria 1 and 7 together: a tracker whose Engine is unset and
// whose FileGlob ends in .tf auto-selects the HCL engine (never falling back
// to jq, which would fail to parse HCL as YAML/JSON), and a provider version
// bump in versions.tf across commits produces a Change.
func TestPoller_HCL_AutoDetectsEngineFromGlob_AndTracksProviderVersion(t *testing.T) {
	t.Parallel()

	repoPath := initTFRepo(t)
	writeAndCommitTF(t, repoPath, "versions.tf", versionsTFContent("~> 5.0", ">= 1.5.0"), "init", tfBase)
	writeAndCommitTF(t, repoPath, "versions.tf", versionsTFContent("~> 5.10", ">= 1.5.0"), "bump google provider", tfBase.Add(time.Hour))

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "versions.tf",
		Field:         "google-provider-version",
		ExtractorExpr: "terraform.required_providers.google.version",
		// Engine intentionally unset — must auto-detect hcl from the .tf glob.
		BackfillDays: 3650,
	}

	p := poller.New(src, st)
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	feed, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}
	if len(feed) != 1 {
		t.Fatalf("got %d changes, want 1; feed = %+v", len(feed), feed)
	}

	c := feed[0]
	if c.ChangeType != domain.ChangeTypeModified {
		t.Errorf("ChangeType = %q, want modified", c.ChangeType)
	}
	if c.OldValue == nil || *c.OldValue != "~> 5.0" {
		t.Errorf("OldValue = %v, want \"~> 5.0\"", c.OldValue)
	}
	if c.NewValue == nil || *c.NewValue != "~> 5.10" {
		t.Errorf("NewValue = %v, want \"~> 5.10\"", c.NewValue)
	}
}

func lockHCLContent(version string) string {
	return `provider "registry.terraform.io/hashicorp/google" {
  version     = "` + version + `"
  constraints = "~> 5.0"
}
`
}

// TestPoller_HCL_TracksLockfileProviderPin proves acceptance criterion 2: a
// provider pin change in .terraform.lock.hcl across commits produces a
// Change, via glob-inferred engine selection on the lockfile's own suffix.
func TestPoller_HCL_TracksLockfileProviderPin(t *testing.T) {
	t.Parallel()

	repoPath := initTFRepo(t)
	writeAndCommitTF(t, repoPath, ".terraform.lock.hcl", lockHCLContent("5.10.0"), "init", tfBase)
	writeAndCommitTF(t, repoPath, ".terraform.lock.hcl", lockHCLContent("5.11.0"), "bump lockfile pin", tfBase.Add(time.Hour))

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      ".terraform.lock.hcl",
		Field:         "google-provider-pin",
		ExtractorExpr: `provider["registry.terraform.io/hashicorp/google"].version`,
		BackfillDays:  3650,
	}

	p := poller.New(src, st)
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	feed, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}
	if len(feed) != 1 {
		t.Fatalf("got %d changes, want 1; feed = %+v", len(feed), feed)
	}
	if feed[0].OldValue == nil || *feed[0].OldValue != "5.10.0" || feed[0].NewValue == nil || *feed[0].NewValue != "5.11.0" {
		t.Errorf("change = %v -> %v, want 5.10.0 -> 5.11.0", feed[0].OldValue, feed[0].NewValue)
	}
}

func moduleTFContent(source, version string) string {
	return `module "vpc" {
  source  = "` + source + `"
  version = "` + version + `"
}
`
}

// TestPoller_HCL_TracksModuleSourceAndVersion proves acceptance criterion 3:
// a module source or version change produces a Change.
func TestPoller_HCL_TracksModuleSourceAndVersion(t *testing.T) {
	t.Parallel()

	repoPath := initTFRepo(t)
	writeAndCommitTF(t, repoPath, "modules.tf", moduleTFContent("terraform-google-modules/network/google", "~> 7.0"), "init", tfBase)
	writeAndCommitTF(t, repoPath, "modules.tf", moduleTFContent("terraform-google-modules/network/google", "~> 8.0"), "bump module version", tfBase.Add(time.Hour))

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "modules.tf",
		Field:         "vpc-module-version",
		ExtractorExpr: "module.vpc.version",
		BackfillDays:  3650,
	}

	p := poller.New(src, st)
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	feed, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}
	if len(feed) != 1 {
		t.Fatalf("got %d changes, want 1; feed = %+v", len(feed), feed)
	}
	if feed[0].OldValue == nil || *feed[0].OldValue != "~> 7.0" || feed[0].NewValue == nil || *feed[0].NewValue != "~> 8.0" {
		t.Errorf("change = %v -> %v, want \"~> 7.0\" -> \"~> 8.0\"", feed[0].OldValue, feed[0].NewValue)
	}
}

// TestPoller_HCL_TracksRequiredVersion proves acceptance criterion 4: a
// required_version change produces a Change.
func TestPoller_HCL_TracksRequiredVersion(t *testing.T) {
	t.Parallel()

	repoPath := initTFRepo(t)
	writeAndCommitTF(t, repoPath, "versions.tf", versionsTFContent("~> 5.0", ">= 1.5.0"), "init", tfBase)
	writeAndCommitTF(t, repoPath, "versions.tf", versionsTFContent("~> 5.0", ">= 1.7.0"), "bump required_version", tfBase.Add(time.Hour))

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "versions.tf",
		Field:         "terraform-required-version",
		ExtractorExpr: "terraform.required_version",
		BackfillDays:  3650,
	}

	p := poller.New(src, st)
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	feed, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}
	if len(feed) != 1 {
		t.Fatalf("got %d changes, want 1; feed = %+v", len(feed), feed)
	}
	if feed[0].OldValue == nil || *feed[0].OldValue != ">= 1.5.0" || feed[0].NewValue == nil || *feed[0].NewValue != ">= 1.7.0" {
		t.Errorf("change = %v -> %v, want \">= 1.5.0\" -> \">= 1.7.0\"", feed[0].OldValue, feed[0].NewValue)
	}
}

func nodePoolTFContent(machineType string) string {
	return `resource "google_container_node_pool" "primary" {
  cluster = google_container_cluster.primary.name

  node_config {
    machine_type = "` + machineType + `"
  }
}
`
}

// TestPoller_HCL_TracksNamedResourceAttribute proves acceptance criterion 5:
// a named resource attribute change (here, a node-pool machine type standing
// in for the PRD's node-pool size dogfood example) produces a Change, and an
// attribute whose value is an HCL expression (the resource's "cluster"
// traversal reference) is captured as its expression text, never evaluated.
func TestPoller_HCL_TracksNamedResourceAttribute(t *testing.T) {
	t.Parallel()

	repoPath := initTFRepo(t)
	writeAndCommitTF(t, repoPath, "node_pool.tf", nodePoolTFContent("e2-medium"), "init", tfBase)
	writeAndCommitTF(t, repoPath, "node_pool.tf", nodePoolTFContent("e2-standard-4"), "resize node pool", tfBase.Add(time.Hour))

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "node_pool.tf",
		Field:         "node-pool-machine-type",
		ExtractorExpr: "resource.google_container_node_pool.primary.node_config.machine_type",
		BackfillDays:  3650,
	}

	p := poller.New(src, st)
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	feed, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}
	if len(feed) != 1 {
		t.Fatalf("got %d changes, want 1; feed = %+v", len(feed), feed)
	}
	if feed[0].OldValue == nil || *feed[0].OldValue != "e2-medium" || feed[0].NewValue == nil || *feed[0].NewValue != "e2-standard-4" {
		t.Errorf("change = %v -> %v, want e2-medium -> e2-standard-4", feed[0].OldValue, feed[0].NewValue)
	}

	// The expression-valued "cluster" attribute must surface as its raw
	// expression text, never a resolved/evaluated value. A dedicated
	// single-commit repo is used (rather than reusing repoPath's two commits,
	// where "cluster" never changes and so would legitimately produce no
	// Change) so this appears as an "added" Change carrying the traversal's
	// source text, proving Extract captured the text rather than failing or
	// evaluating it.
	exprRepoPath := initTFRepo(t)
	writeAndCommitTF(t, exprRepoPath, "node_pool.tf", nodePoolTFContent("e2-medium"), "init", tfBase)
	exprSrc, err := gitsource.Open(exprRepoPath)
	if err != nil {
		t.Fatalf("gitsource.Open (expr repo): %v", err)
	}
	st2 := newTestStore(t)
	exprTracker := domain.Tracker{
		Repo:          exprRepoPath,
		FileGlob:      "node_pool.tf",
		Field:         "node-pool-cluster-ref",
		ExtractorExpr: "resource.google_container_node_pool.primary.cluster",
		BackfillDays:  3650,
	}
	p2 := poller.New(exprSrc, st2)
	if err := p2.Poll(exprTracker); err != nil {
		t.Fatalf("Poll (expr field): %v", err)
	}
	exprFeed, err := st2.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed (expr field): %v", err)
	}
	if len(exprFeed) != 1 {
		t.Fatalf("expr field: got %d changes, want 1; feed = %+v", len(exprFeed), exprFeed)
	}
	if exprFeed[0].ChangeType != domain.ChangeTypeAdded {
		t.Errorf("expr field ChangeType = %q, want added", exprFeed[0].ChangeType)
	}
	if exprFeed[0].NewValue == nil || *exprFeed[0].NewValue != "google_container_cluster.primary.name" {
		t.Errorf("expr field NewValue = %v, want raw expression text \"google_container_cluster.primary.name\"", exprFeed[0].NewValue)
	}
}

// TestPoller_HCL_AbsentTraversal_YieldsNoChangeAndNoError proves acceptance
// criterion 6: a traversal path that matches nothing in otherwise
// well-formed HCL is treated as absent — no Change, and Poll returns nil.
func TestPoller_HCL_AbsentTraversal_YieldsNoChangeAndNoError(t *testing.T) {
	t.Parallel()

	repoPath := initTFRepo(t)
	writeAndCommitTF(t, repoPath, "node_pool.tf", nodePoolTFContent("e2-medium"), "init", tfBase)
	writeAndCommitTF(t, repoPath, "node_pool.tf", nodePoolTFContent("e2-standard-4"), "resize node pool", tfBase.Add(time.Hour))

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "node_pool.tf",
		Field:         "node-pool-disk-size",
		ExtractorExpr: "resource.google_container_node_pool.primary.node_config.disk_size_gb", // never set
		BackfillDays:  3650,
	}

	p := poller.New(src, st)
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	feed, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}
	if len(feed) != 0 {
		t.Fatalf("got %d changes, want 0 (absent path); feed = %+v", len(feed), feed)
	}
}

const malformedTF = `resource "google_container_node_pool" "primary" {
  node_config {
    machine_type = "e2-medium"
` // missing closing braces — unparseable

// TestPoller_HCL_MalformedFile_SkippedWithoutDroppingOtherFiles proves
// acceptance criterion 8: a malformed .tf file matched by a fan-out glob is
// skipped (its extraction error is reported, not swallowed, and Poll does
// not panic) WITHOUT dropping the other, valid, matched file's processing in
// the same tracker poll cycle.
func TestPoller_HCL_MalformedFile_SkippedWithoutDroppingOtherFiles(t *testing.T) {
	t.Parallel()

	repoPath := initTFRepo(t)
	// "bad.tf" is malformed from its very first (only) commit.
	writeAndCommitTF(t, repoPath, "bad.tf", malformedTF, "bad init", tfBase)
	// "good.tf" is well-formed and changes across two commits.
	writeAndCommitTF(t, repoPath, "good.tf", nodePoolTFContent("e2-medium"), "good init", tfBase.Add(time.Hour))
	writeAndCommitTF(t, repoPath, "good.tf", nodePoolTFContent("e2-standard-4"), "good resize", tfBase.Add(2*time.Hour))

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "*.tf",
		Field:         "node-pool-machine-type",
		ExtractorExpr: "resource.google_container_node_pool.primary.node_config.machine_type",
		BackfillDays:  3650,
	}

	p := poller.New(src, st)

	// Must not panic — a bare call is itself part of the assertion; a panic
	// fails the test regardless of the recover below.
	var pollErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Poll panicked on a malformed file: %v", r)
			}
		}()
		pollErr = p.Poll(tracker)
	}()

	// The malformed file's failure must still be reported (never silently
	// swallowed) — Poll returns a non-nil error naming it.
	if pollErr == nil {
		t.Fatal("Poll returned nil, want a reported error for the malformed bad.tf")
	}
	if !strings.Contains(pollErr.Error(), "bad.tf") {
		t.Errorf("Poll error = %q, want it to name the failing file bad.tf", pollErr.Error())
	}

	// The other (valid) file's Change must still have been processed and
	// persisted — the malformed file must not drop the rest of the cycle.
	feed, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}
	if len(feed) != 1 {
		t.Fatalf("got %d changes, want 1 (good.tf's change survives bad.tf's failure); feed = %+v", len(feed), feed)
	}
	if feed[0].FilePath != "good.tf" {
		t.Errorf("surviving change FilePath = %q, want good.tf", feed[0].FilePath)
	}
	if feed[0].OldValue == nil || *feed[0].OldValue != "e2-medium" || feed[0].NewValue == nil || *feed[0].NewValue != "e2-standard-4" {
		t.Errorf("surviving change = %v -> %v, want e2-medium -> e2-standard-4", feed[0].OldValue, feed[0].NewValue)
	}
}

// fakeExtractFailureRecorder is a test double satisfying
// poller.ExtractFailureRecorder without importing pollstatus — proving the
// Poller depends on the interface, not the concrete Registry.
type fakeExtractFailureRecorder struct {
	mu     sync.Mutex
	counts map[string]int
}

func newFakeExtractFailureRecorder() *fakeExtractFailureRecorder {
	return &fakeExtractFailureRecorder{counts: make(map[string]int)}
}

func (f *fakeExtractFailureRecorder) RecordExtractFailure(engine string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts[engine]++
}

func (f *fakeExtractFailureRecorder) count(engine string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[engine]
}

// TestPoller_HCL_MalformedFile_ReportsExtractFailureOnPollHealthSurface
// proves the first half of acceptance criterion 9: an HCL parse failure
// during Poll is reported to the poll-health/status surface (here, an
// injected recorder standing in for pollstatus.Registry — see
// poller.WithExtractFailureRecorder), tagged with the "hcl" engine.
func TestPoller_HCL_MalformedFile_ReportsExtractFailureOnPollHealthSurface(t *testing.T) {
	t.Parallel()

	repoPath := initTFRepo(t)
	writeAndCommitTF(t, repoPath, "bad.tf", malformedTF, "bad init", tfBase)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)

	rec := newFakeExtractFailureRecorder()
	p := poller.New(src, st, poller.WithExtractFailureRecorder(rec))

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "bad.tf",
		Field:         "node-pool-machine-type",
		ExtractorExpr: "resource.google_container_node_pool.primary.node_config.machine_type",
		BackfillDays:  3650,
	}
	if err := p.Poll(tracker); err == nil {
		t.Fatal("Poll returned nil, want an error for the malformed file")
	}

	if got := rec.count("hcl"); got != 1 {
		t.Errorf("ExtractFailureRecorder count for engine %q = %d, want 1", "hcl", got)
	}
}

// TestPoller_HCL_Poll_EmitsREDSignals proves the first half of acceptance
// criterion 9: a successful poll cycle for an HCL tracker emits the same RED
// signals (request counter, zero error count) the observability foundation
// established for every other engine — the instrumentation wraps Poll
// itself and is engine-agnostic, so an HCL tracker is covered without any
// HCL-specific instrumentation code. Mirrors
// TestPoller_Poll_EmitsREDSignals (the jq-engine equivalent in
// poller_telemetry_test.go).
func TestPoller_HCL_Poll_EmitsREDSignals(t *testing.T) {
	t.Parallel()

	repoPath := initTFRepo(t)
	writeAndCommitTF(t, repoPath, "versions.tf", versionsTFContent("~> 5.0", ">= 1.5.0"), "init", tfBase)
	writeAndCommitTF(t, repoPath, "versions.tf", versionsTFContent("~> 5.10", ">= 1.5.0"), "bump", tfBase.Add(time.Hour))

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	p := poller.New(src, st, poller.WithMeterProvider(mp))

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "versions.tf",
		Field:         "google-provider-version",
		ExtractorExpr: "terraform.required_providers.google.version",
		BackfillDays:  3650,
	}
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := findSum(rm, "operation.requests"); got != 1 {
		t.Errorf("operation.requests = %d, want 1", got)
	}
	if got := findSum(rm, "operation.errors"); got != 0 {
		t.Errorf("operation.errors = %d, want 0", got)
	}
}

// TestPoller_HCL_Poll_Failure_EmitsErrorSignalAndTraceCorrelatedLog proves
// the second half of acceptance criterion 9: an HCL tracker's poll failure
// (the malformed-file fixture) is counted as a RED error and logged as a
// single-line JSON entry correlated to the poll cycle's trace — the same
// contract poller_telemetry_test.go proves for the jq engine.
func TestPoller_HCL_Poll_Failure_EmitsErrorSignalAndTraceCorrelatedLog(t *testing.T) {
	t.Parallel()

	repoPath := initTFRepo(t)
	writeAndCommitTF(t, repoPath, "bad.tf", malformedTF, "bad init", tfBase)
	writeAndCommitTF(t, repoPath, "good.tf", nodePoolTFContent("e2-medium"), "good init", tfBase.Add(time.Hour))
	writeAndCommitTF(t, repoPath, "good.tf", nodePoolTFContent("e2-standard-4"), "good resize", tfBase.Add(2*time.Hour))

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = tp.Shutdown(context.Background()) }()

	var buf strings.Builder
	logger := telemetry.NewLogger("change-tracking-dashboard", &buf)

	p := poller.New(src, st, poller.WithMeterProvider(mp), poller.WithTracerProvider(tp), poller.WithLogger(logger))

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "*.tf",
		Field:         "node-pool-machine-type",
		ExtractorExpr: "resource.google_container_node_pool.primary.node_config.machine_type",
		BackfillDays:  3650,
	}
	if err := p.Poll(tracker); err == nil {
		t.Fatal("Poll returned nil, want an error for the malformed bad.tf")
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := findSum(rm, "operation.errors"); got != 1 {
		t.Errorf("operation.errors = %d, want 1", got)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatal("expected at least one structured log line during the failing HCL poll cycle")
	}
	for _, line := range lines {
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatalf("log line not JSON: %v; line: %s", err, line)
		}
		if fields["service.name"] != "change-tracking-dashboard" {
			t.Errorf("service.name = %v, want change-tracking-dashboard", fields["service.name"])
		}
		if fields["trace_id"] == nil || fields["trace_id"] == "" {
			t.Errorf("log line missing trace_id: %v", fields)
		}
		if fields["span_id"] == nil || fields["span_id"] == "" {
			t.Errorf("log line missing span_id: %v", fields)
		}
	}
}
