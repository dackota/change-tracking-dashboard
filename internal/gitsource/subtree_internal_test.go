package gitsource

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzSecurePath asserts the path-containment invariant that must hold for
// every possible git tree entry name, including adversarial ones an attacker
// controlling repository content could plant: whenever securePath succeeds,
// the returned destination path must lie strictly within destDir. securePath
// is the sole safeguard MaterializeSubtree relies on to reject a
// path-traversal or absolute-path tree entry (Zip-Slip class) before writing
// any file, so this fuzzes it directly rather than only tabulating examples.
func FuzzSecurePath(f *testing.F) {
	seeds := []string{
		"charts/dep.tgz",
		"Chart.yaml",
		"../escape",
		"../../etc/x",
		"/etc/passwd",
		"a/../../b",
		"..",
		".",
		"",
		"a/../../../../../../etc/passwd",
		"dir/../../dir/../../../escape",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, relPath string) {
		destDir := filepath.Join(t.TempDir(), "dest")

		got, err := securePath(destDir, relPath)
		if err != nil {
			// Rejected — the invariant holds vacuously; nothing was returned
			// to write to.
			return
		}

		cleanDest := filepath.Clean(destDir)
		if got != cleanDest && !strings.HasPrefix(got, cleanDest+string(filepath.Separator)) {
			t.Fatalf("securePath(%q, %q) = %q, which escapes destDir %q", destDir, relPath, got, cleanDest)
		}
		if got == cleanDest {
			t.Fatalf("securePath(%q, %q) = %q, resolves to destDir itself (not a file path)", destDir, relPath, got)
		}
	})
}

// TestSecurePath_AdversarialExamples documents the specific ".."-traversal
// shapes the fuzz corpus seeds, as readable examples: every one of these
// resolves (via filepath.Clean) to a location outside — or exactly at —
// destDir, so securePath must reject all of them outright.
func TestSecurePath_AdversarialExamples(t *testing.T) {
	t.Parallel()

	destDir := filepath.Join(t.TempDir(), "dest")

	adversarial := []string{
		"../escape",
		"../../etc/x",
		"a/../../b",
		"..",
		".",
	}

	for _, relPath := range adversarial {
		if _, err := securePath(destDir, relPath); err == nil {
			t.Errorf("securePath(%q, %q) = nil error, want rejection (escapes destDir)", destDir, relPath)
		}
	}
}

// TestSecurePath_AbsolutePathIsContainedNotRejected documents that an
// entry name shaped like an absolute path is not a traversal in Go's
// filepath.Join semantics — Join always treats every argument as relative
// to what precedes it, so "/etc/passwd" resolves to destDir/etc/passwd, not
// to the real /etc/passwd. securePath therefore accepts it, and the
// resulting path is still safely contained within destDir (proven directly
// here, and as the universal invariant in FuzzSecurePath).
func TestSecurePath_AbsolutePathIsContainedNotRejected(t *testing.T) {
	t.Parallel()

	destDir := filepath.Join(t.TempDir(), "dest")

	got, err := securePath(destDir, "/etc/passwd")
	if err != nil {
		t.Fatalf("securePath(%q, %q) returned error: %v", destDir, "/etc/passwd", err)
	}

	want := filepath.Join(filepath.Clean(destDir), "etc", "passwd")
	if got != want {
		t.Errorf("securePath(%q, %q) = %q, want %q (contained under destDir)", destDir, "/etc/passwd", got, want)
	}
}

// TestSecurePath_SafePathsAreAccepted documents that ordinary, well-formed
// relative paths — the shape every real vendored chart file has — resolve
// inside destDir without error.
func TestSecurePath_SafePathsAreAccepted(t *testing.T) {
	t.Parallel()

	destDir := filepath.Join(t.TempDir(), "dest")

	safe := []string{
		"Chart.yaml",
		"values.yaml",
		"charts/dep.tgz",
		"templates/nested/deployment.yaml",
	}

	for _, relPath := range safe {
		got, err := securePath(destDir, relPath)
		if err != nil {
			t.Errorf("securePath(%q, %q) returned error, want acceptance: %v", destDir, relPath, err)
			continue
		}
		want := filepath.Join(filepath.Clean(destDir), relPath)
		if got != want {
			t.Errorf("securePath(%q, %q) = %q, want %q", destDir, relPath, got, want)
		}
	}
}
