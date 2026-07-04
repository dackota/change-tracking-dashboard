package gitsource_test

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
)

// generousBounds is large enough that no fixture in this file should ever
// hit it, isolating "within bounds" tests from the ceiling behavior under
// test elsewhere in this file.
var generousBounds = gitsource.MaterializeBounds{
	MaxTotalBytes: 10 << 20, // 10 MiB
	MaxFiles:      1000,
	MaxDepth:      50,
}

// TestMaterializeSubtreeBounded_WithinBounds_MatchesUnboundedBehavior proves
// MaterializeSubtreeBounded behaves exactly like MaterializeSubtree when the
// subtree is well within the configured ceilings — the bounded variant is a
// strict superset of behavior, not a different extraction.
func TestMaterializeSubtreeBounded_WithinBounds_MatchesUnboundedBehavior(t *testing.T) {
	t.Parallel()

	files := map[string][]byte{
		"Chart.yaml":     []byte("apiVersion: v2\nname: tenant\nversion: 1.0.0\n"),
		"values.yaml":    []byte("replicaCount: 1\n"),
		"charts/dep.tgz": {0x1f, 0x8b, 0x08, 0x00, 0x01, 0x02, 0x03},
	}
	repoPath, sha := buildTenantChartRepo(t, files)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	destDir := t.TempDir()
	if err := src.MaterializeSubtreeBounded(sha, "tenant", destDir, generousBounds); err != nil {
		t.Fatalf("MaterializeSubtreeBounded: %v", err)
	}

	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(destDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read materialized %q: %v", rel, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("materialized %q = %v, want %v", rel, got, want)
		}
	}
}

// TestMaterializeSubtreeBounded_ExceedsMaxTotalBytes_ReturnsBoundsError
// proves the byte ceiling is enforced: a subtree whose total content exceeds
// MaxTotalBytes is rejected with ErrMaterializeBoundsExceeded, and the
// oversized file is never written (materializeBlob checks the ceiling
// before copying any of that file's bytes).
func TestMaterializeSubtreeBounded_ExceedsMaxTotalBytes_ReturnsBoundsError(t *testing.T) {
	t.Parallel()

	files := map[string][]byte{
		"Chart.yaml":     []byte("apiVersion: v2\nname: tenant\nversion: 1.0.0\n"),
		"charts/dep.tgz": bytes.Repeat([]byte{0xAB}, 1024), // 1 KiB
	}
	repoPath, sha := buildTenantChartRepo(t, files)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	destDir := t.TempDir()
	bounds := gitsource.MaterializeBounds{MaxTotalBytes: 512, MaxFiles: 1000, MaxDepth: 50}
	err = src.MaterializeSubtreeBounded(sha, "tenant", destDir, bounds)
	if err == nil {
		t.Fatal("MaterializeSubtreeBounded with an oversized subtree returned nil error, want ErrMaterializeBoundsExceeded")
	}
	if !errors.Is(err, gitsource.ErrMaterializeBoundsExceeded) {
		t.Errorf("error = %v, want errors.Is(err, ErrMaterializeBoundsExceeded) == true", err)
	}

	if _, statErr := os.Stat(filepath.Join(destDir, "charts", "dep.tgz")); !os.IsNotExist(statErr) {
		t.Errorf("oversized file was written despite exceeding MaxTotalBytes (stat err: %v)", statErr)
	}
}

// TestMaterializeSubtreeBounded_ExceedsMaxFiles_ReturnsBoundsError proves the
// file-count ceiling is enforced independently of total byte size.
func TestMaterializeSubtreeBounded_ExceedsMaxFiles_ReturnsBoundsError(t *testing.T) {
	t.Parallel()

	files := map[string][]byte{"Chart.yaml": []byte("apiVersion: v2\nname: tenant\nversion: 1.0.0\n")}
	for i := 0; i < 10; i++ {
		files[fmt.Sprintf("templates/t%d.yaml", i)] = []byte("kind: ConfigMap\n")
	}
	repoPath, sha := buildTenantChartRepo(t, files)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	destDir := t.TempDir()
	bounds := gitsource.MaterializeBounds{MaxTotalBytes: 10 << 20, MaxFiles: 3, MaxDepth: 50}
	err = src.MaterializeSubtreeBounded(sha, "tenant", destDir, bounds)
	if err == nil {
		t.Fatal("MaterializeSubtreeBounded with 11 files and MaxFiles=3 returned nil error, want ErrMaterializeBoundsExceeded")
	}
	if !errors.Is(err, gitsource.ErrMaterializeBoundsExceeded) {
		t.Errorf("error = %v, want errors.Is(err, ErrMaterializeBoundsExceeded) == true", err)
	}
}

// TestMaterializeSubtreeBounded_ExceedsMaxDepth_ReturnsBoundsError proves the
// recursion-depth ceiling is enforced, guarding against stack exhaustion from
// a maliciously deep git tree.
func TestMaterializeSubtreeBounded_ExceedsMaxDepth_ReturnsBoundsError(t *testing.T) {
	t.Parallel()

	files := map[string][]byte{
		"Chart.yaml":          []byte("apiVersion: v2\nname: tenant\nversion: 1.0.0\n"),
		"a/b/c/d/e/deep.yaml": []byte("kind: ConfigMap\n"),
	}
	repoPath, sha := buildTenantChartRepo(t, files)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	destDir := t.TempDir()
	bounds := gitsource.MaterializeBounds{MaxTotalBytes: 10 << 20, MaxFiles: 1000, MaxDepth: 2}
	err = src.MaterializeSubtreeBounded(sha, "tenant", destDir, bounds)
	if err == nil {
		t.Fatal("MaterializeSubtreeBounded with depth-5 nesting and MaxDepth=2 returned nil error, want ErrMaterializeBoundsExceeded")
	}
	if !errors.Is(err, gitsource.ErrMaterializeBoundsExceeded) {
		t.Errorf("error = %v, want errors.Is(err, ErrMaterializeBoundsExceeded) == true", err)
	}
}

// TestMaterializeSubtreeBounded_MaliciousTreeEntry_StillContained proves the
// bounded variant retains the Zip-Slip containment guarantee: bounds
// enforcement is additive, not a replacement for path containment.
func TestMaterializeSubtreeBounded_MaliciousTreeEntry_StillContained(t *testing.T) {
	t.Parallel()

	repoPath, sha := buildMaliciousSubtreeRepo(t)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	base := t.TempDir()
	destDir := filepath.Join(base, "dest")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("mkdir destDir: %v", err)
	}

	err = src.MaterializeSubtreeBounded(sha, "tenant", destDir, generousBounds)
	if err == nil {
		t.Fatal("MaterializeSubtreeBounded with a path-traversal tree entry returned nil error, want rejection")
	}
	if errors.Is(err, gitsource.ErrMaterializeBoundsExceeded) {
		t.Errorf("error = %v, want a containment error, not a bounds error", err)
	}
}

// materializedTotalBytes sums the size of every regular file under root.
func materializedTotalBytes(t *testing.T, root string) int64 {
	t.Helper()

	var total int64
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk materialized dir: %v", err)
	}
	return total
}

// boundedFileSet is a randomized set of distinct relative file paths paired
// with content, used to drive the materialization-bounds property test. Each
// entry's content length is also randomized so the generated total size
// varies from well under to well over a small configured ceiling.
type boundedFileSet map[string][]byte

// Generate implements quick.Generator, producing between 1 and 8 files, each
// 0-300 bytes, so the generated total is sometimes within and sometimes
// beyond the small ceiling the property test configures.
func (boundedFileSet) Generate(rnd *rand.Rand, size int) reflect.Value {
	n := rnd.Intn(8) + 1
	files := make(boundedFileSet, n)
	for i := 0; i < n; i++ {
		content := make([]byte, rnd.Intn(300))
		rnd.Read(content)
		files[fmt.Sprintf("file%d.bin", i)] = content
	}
	return reflect.ValueOf(files)
}

// TestMaterializeSubtreeBounded_NeverExceedsConfiguredCeiling_Property
// asserts the invariant that must hold for every possible subtree content:
// bounded materialization either succeeds with the true total on disk at or
// under the configured byte ceiling, or fails with
// ErrMaterializeBoundsExceeded — and in the failure case, the bytes actually
// written to destDir never exceed the ceiling either. This is the
// materialization-bounds contract the chartdiff orchestrator relies on to
// classify a hostile/oversized tenant subtree as ExceededLimits without ever
// letting it exhaust host disk first.
func TestMaterializeSubtreeBounded_NeverExceedsConfiguredCeiling_Property(t *testing.T) {
	t.Parallel()

	const maxTotalBytes = 512

	property := func(files boundedFileSet) bool {
		if len(files) == 0 {
			return true
		}
		seeded := map[string][]byte{
			"Chart.yaml": []byte("apiVersion: v2\nname: tenant\nversion: 1.0.0\n"),
		}
		for rel, content := range files {
			seeded[rel] = content
		}

		repoPath, sha := buildTenantChartRepo(t, seeded)
		src, err := gitsource.Open(repoPath)
		if err != nil {
			t.Fatalf("gitsource.Open: %v", err)
		}

		destDir := t.TempDir()
		bounds := gitsource.MaterializeBounds{MaxTotalBytes: maxTotalBytes, MaxFiles: 1000, MaxDepth: 50}
		err = src.MaterializeSubtreeBounded(sha, "tenant", destDir, bounds)

		total := materializedTotalBytes(t, destDir)
		if total > maxTotalBytes {
			t.Logf("materialized %d bytes, exceeding ceiling %d (err=%v)", total, maxTotalBytes, err)
			return false
		}

		if err != nil && !errors.Is(err, gitsource.ErrMaterializeBoundsExceeded) {
			t.Logf("unexpected non-bounds error: %v", err)
			return false
		}

		return true
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 40}); err != nil {
		t.Error(err)
	}
}

// TestMaterializeSubtree_StillWorksUnbounded is a regression check that the
// pre-existing, unbounded MaterializeSubtree entry point (used by callers
// that don't need bounds, and exercised by every test above it in
// subtree_test.go) is untouched by the MaterializeSubtreeBounded addition.
func TestMaterializeSubtree_StillWorksUnbounded(t *testing.T) {
	t.Parallel()

	files := map[string][]byte{"Chart.yaml": []byte("apiVersion: v2\nname: tenant\nversion: 1.0.0\n")}
	repoPath, sha := buildTenantChartRepo(t, files)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	destDir := t.TempDir()
	if err := src.MaterializeSubtree(sha, "tenant", destDir); err != nil {
		t.Fatalf("MaterializeSubtree: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "Chart.yaml"))
	if err != nil {
		t.Fatalf("read Chart.yaml: %v", err)
	}
	if !strings.Contains(string(got), "name: tenant") {
		t.Errorf("Chart.yaml content = %q, want it to contain %q", got, "name: tenant")
	}
}
