package chartrender_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/chartrender"
)

// TestRender_Sprig_EnvAndExpandenvNeverLeakHostSecret proves the PRD's
// Sprig env/expandenv host-secret-exfiltration concern is already closed by
// Helm itself: pkg/engine/funcs.go deletes both "env" and "expandenv" from
// the Sprig func map before building its template.FuncMap, so a template
// calling either one hits an undefined-function template-execution error
// rather than reading the host environment. Render surfaces that as a
// classified malformed-chart failure with no partial Result — the render
// path can never read (and so can never leak) a real host environment
// variable, whether or not the caller's process happens to have one set
// under the name an untrusted chart guesses.
//
// Do NOT "fix" this with os.Clearenv/os.Setenv scrubbing — mutating process
// env is a data race under the orchestrator's concurrent renders. This test
// exists to pin Helm's own (safe) behavior so a future Helm upgrade that
// changed it would be caught here.
func TestRender_Sprig_EnvAndExpandenvNeverLeakHostSecret(t *testing.T) {
	// Not t.Parallel(): blockNetworkAndCluster and t.Setenv (below) both
	// forbid it.
	blockNetworkAndCluster(t)

	const secretEnvVar = "CHARTRENDER_TEST_SECRET"
	const secretValue = "super-secret-value-must-never-appear-in-output"
	t.Setenv(secretEnvVar, secretValue)

	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "Chart.yaml"), "apiVersion: v2\nname: env-probe-chart\nversion: 0.1.0\n")
	mustWriteFile(t, filepath.Join(dir, "templates", "probe.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: env-probe-cm
data:
  fromEnv: {{ env "`+secretEnvVar+`" | quote }}
  fromExpandenv: {{ expandenv "$`+secretEnvVar+`" | quote }}
`)

	result, err := chartrender.Render(dir, nil)

	if result != nil {
		if strings.Contains(result.Normalized(), secretValue) {
			t.Fatalf("Normalized() leaked the host secret env var:\n%s", result.Normalized())
		}
		t.Fatalf("Render result = %+v, want nil — env/expandenv must never produce a partial render", result)
	}
	if err == nil {
		t.Fatal("Render err = nil, want an error: env/expandenv are undefined functions in Helm's template func map")
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Fatalf("Render err leaked the host secret env var: %v", err)
	}

	var failure *chartrender.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("Render err = %v (%T), want *chartrender.Failure", err, err)
	}
	if failure.Reason != chartrender.ReasonMalformedChart {
		t.Errorf("failure.Reason = %q, want %q", failure.Reason, chartrender.ReasonMalformedChart)
	}
}
