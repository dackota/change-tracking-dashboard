package chartdiff_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/chartdiff"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// envProbeVarName is the process environment variable name the probe
// template looks up. Its value is randomized per property iteration by
// TestDiff_EnvNeverLeaksIntoOutcome_Property.
const envProbeVarName = "CHARTDIFF_TEST_ENV_LEAK_PROBE"

// buildEnvLeakProbeRepo builds a two-commit temp git repo whose tenant chart
// template references {{ env "<envProbeVarName>" }} on both commits, so
// Engine.Diff always has a real first parent to resolve (never
// NoPriorVersion) and always attempts a real chartrender.Render that
// references the process environment.
func buildEnvLeakProbeRepo(t *testing.T) (repoPath, sha2 string) {
	t.Helper()

	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}

	tenantDir := filepath.Join(dir, "tenant")
	probeTemplate := `apiVersion: v1
kind: ConfigMap
metadata:
  name: env-probe-cm
data:
  fromEnv: {{ env "` + envProbeVarName + `" | quote }}
`
	writeVersion := func(version string) {
		realTestWriteFile(t, filepath.Join(tenantDir, "Chart.yaml"), "apiVersion: v2\nname: umbrella\nversion: "+version+"\n")
		realTestWriteFile(t, filepath.Join(tenantDir, "values.yaml"), "message: hello-tenant\n")
		realTestWriteFile(t, filepath.Join(tenantDir, "templates", "probe.yaml"), probeTemplate)
	}

	writeVersion("0.1.0")
	addAll(t, wt, dir)
	_, err = wt.Commit("chore: seed tenant chart with env probe template", &git.CommitOptions{
		Author: &object.Signature{Name: "alice", Email: "alice@example.com", When: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("commit 1: %v", err)
	}

	writeVersion("0.2.0")
	addAll(t, wt, dir)
	c2, err := wt.Commit("chore: bump version, template still probes env", &git.CommitOptions{
		Author: &object.Signature{Name: "bob", Email: "bob@example.com", When: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("commit 2: %v", err)
	}

	return dir, c2.String()
}

// TestDiff_EnvNeverLeaksIntoOutcome_Property asserts the invariant that must
// hold for every possible value the process happens to have set for the
// variable an untrusted chart template probes via Sprig's env func: no
// matter the secret's value, Engine.Diff — exercised through the real
// gitsource.Source and the real chartrender.Render, no fakes — never returns
// an OK Outcome for a template that calls env, and the secret value never
// appears anywhere in the returned Outcome. This pins the property at the
// chartdiff public interface, on top of chartrender's own unit-level proof
// (sprig_env_test.go) that env/expandenv are undefined functions in Helm's
// template func map: a template invoking either one always fails closed to
// CouldNotRender, so there is no code path — today or after an engine-level
// change to this package — that could surface a host secret in a Chart
// diff's output.
func TestDiff_EnvNeverLeaksIntoOutcome_Property(t *testing.T) {
	// Not t.Parallel(): mutates the real process environment across
	// property iterations.

	repoPath, sha2 := buildEnvLeakProbeRepo(t)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	property := func(secretSuffix uint32) bool {
		secretValue := fmt.Sprintf("chartdiff-secret-%d-must-never-leak", secretSuffix)

		if err := os.Setenv(envProbeVarName, secretValue); err != nil {
			t.Fatalf("os.Setenv: %v", err)
		}
		defer func() {
			if err := os.Unsetenv(envProbeVarName); err != nil {
				t.Fatalf("os.Unsetenv: %v", err)
			}
		}()

		// A fresh Engine per iteration: the cache is keyed on
		// (repo, tenant, parent, commit), which doesn't vary across
		// iterations here (only the env value does), so a shared Engine
		// would serve the first iteration's cached CouldNotRender outcome
		// forever and never actually re-render against the new secret.
		engine, err := chartdiff.NewEngine(chartdiff.Config{}, nil)
		if err != nil {
			t.Fatalf("NewEngine: %v", err)
		}

		outcome := engine.Diff(context.Background(), src, chartdiff.Request{
			RepoName:   "tenant-repo",
			TenantPath: "tenant",
			CommitSha:  sha2,
		})

		if outcome.Kind == chartdiff.OK {
			t.Logf("outcome.Kind = OK for an env-probing template, want a failure classification (env must never successfully resolve)")
			return false
		}
		if strings.Contains(outcome.Diff.Unified, secretValue) {
			t.Logf("outcome.Diff.Unified leaked the secret value: %q", outcome.Diff.Unified)
			return false
		}
		if strings.Contains(fmt.Sprintf("%+v", outcome), secretValue) {
			t.Logf("outcome leaked the secret value somewhere in its fields: %+v", outcome)
			return false
		}
		return true
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 15}); err != nil {
		t.Error(err)
	}
}
