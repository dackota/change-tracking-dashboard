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
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// generousBounds is large enough that no fixture in this file should ever
// hit it, isolating "within bounds" tests from the ceiling behavior under
// test elsewhere in this file.
var generousBounds = gitsource.MaterializeBounds{
	MaxTotalBytes: 10 << 20, // 10 MiB
	MaxFiles:      1000,
	MaxDepth:      50,
	MaxTreeNodes:  1000,
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
	bounds := gitsource.MaterializeBounds{MaxTotalBytes: 512, MaxFiles: 1000, MaxDepth: 50, MaxTreeNodes: 1000}
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
	bounds := gitsource.MaterializeBounds{MaxTotalBytes: 10 << 20, MaxFiles: 3, MaxDepth: 50, MaxTreeNodes: 1000}
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
	bounds := gitsource.MaterializeBounds{MaxTotalBytes: 10 << 20, MaxFiles: 1000, MaxDepth: 2, MaxTreeNodes: 1000}
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
		bounds := gitsource.MaterializeBounds{MaxTotalBytes: maxTotalBytes, MaxFiles: 1000, MaxDepth: 50, MaxTreeNodes: 1000}
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

// buildManyEmptyDirsRepo builds a git repo whose "tenant/" subtree contains n
// sibling empty directories (each an empty tree object: zero blobs, zero
// nested subtrees), via direct plumbing object writes rather than the
// worktree Add/Commit flow — git has no concept of a trackable empty
// directory, so the ordinary worktree API cannot stage one; a tree object
// with zero entries is nonetheless a perfectly valid low-level git object,
// which is exactly the adversarial shape MaxTreeNodes must bound: zero bytes,
// zero files, but a real node MaterializeSubtreeBounded must still visit.
// Every sibling directory reuses the same empty-tree object hash (real git
// content-addressing would do the same for identical content), so building a
// large n costs one extra tree-entry append per iteration, not one extra
// object.
func buildManyEmptyDirsRepo(t *testing.T, n int) (repoPath, sha string) {
	t.Helper()

	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}
	store := repo.Storer

	writeTree := func(entries []object.TreeEntry) plumbing.Hash {
		tree := &object.Tree{Entries: entries}
		obj := store.NewEncodedObject()
		if err := tree.Encode(obj); err != nil {
			t.Fatalf("encode tree: %v", err)
		}
		h, err := store.SetEncodedObject(obj)
		if err != nil {
			t.Fatalf("store tree: %v", err)
		}
		return h
	}

	emptyTreeHash := writeTree(nil)

	// Git requires tree entries to be sorted by name; zero-pad so
	// lexicographic and numeric order agree regardless of n's magnitude.
	width := len(fmt.Sprintf("%d", n))
	entries := make([]object.TreeEntry, 0, n)
	for i := 0; i < n; i++ {
		entries = append(entries, object.TreeEntry{Name: fmt.Sprintf("dir%0*d", width, i), Mode: filemode.Dir, Hash: emptyTreeHash})
	}
	tenantTreeHash := writeTree(entries)
	rootTreeHash := writeTree([]object.TreeEntry{{Name: "tenant", Mode: filemode.Dir, Hash: tenantTreeHash}})

	when := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	commit := &object.Commit{
		Author:    object.Signature{Name: "tester", Email: "tester@example.com", When: when},
		Committer: object.Signature{Name: "tester", Email: "tester@example.com", When: when},
		Message:   "many empty dirs",
		TreeHash:  rootTreeHash,
	}
	obj := store.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		t.Fatalf("encode commit: %v", err)
	}
	commitHash, err := store.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("store commit: %v", err)
	}

	return dir, commitHash.String()
}

// TestMaterializeSubtreeBounded_ManyEmptyNestedDirectories_ReturnsBoundsErrorPromptly
// is the concrete adversarial case MaxTreeNodes closes: a tree of far more
// empty sibling directories than the configured ceiling has zero bytes and
// zero files, so neither MaxTotalBytes nor MaxFiles ever trips — without its
// own ceiling, this shape would walk unbounded. MaxTreeNodes must reject it
// promptly (well before visiting all 50,000 entries) rather than fully
// traversing or hanging.
func TestMaterializeSubtreeBounded_ManyEmptyNestedDirectories_ReturnsBoundsErrorPromptly(t *testing.T) {
	t.Parallel()

	const n = 50_000
	repoPath, sha := buildManyEmptyDirsRepo(t, n)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	destDir := t.TempDir()
	bounds := gitsource.MaterializeBounds{MaxTotalBytes: 10 << 20, MaxFiles: 1000, MaxDepth: 50, MaxTreeNodes: 10}

	start := time.Now()
	err = src.MaterializeSubtreeBounded(sha, "tenant", destDir, bounds)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("MaterializeSubtreeBounded over 50,000 empty sibling dirs with MaxTreeNodes=10 returned nil error, want ErrMaterializeBoundsExceeded")
	}
	if !errors.Is(err, gitsource.ErrMaterializeBoundsExceeded) {
		t.Errorf("error = %v, want errors.Is(err, ErrMaterializeBoundsExceeded) == true", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("MaterializeSubtreeBounded took %v to reject an oversized-by-nodes tree, want a prompt rejection (not a full traversal of all %d entries)", elapsed, n)
	}
}

// manyEmptyDirsCount is a randomized sibling-empty-dir count used to drive
// the tree-node-ceiling property test below, kept small enough (0-300) that
// the test itself stays fast while still exercising well-under, right-at, and
// well-over a small configured MaxTreeNodes ceiling.
type manyEmptyDirsCount int

// Generate implements quick.Generator.
func (manyEmptyDirsCount) Generate(rnd *rand.Rand, size int) reflect.Value {
	return reflect.ValueOf(manyEmptyDirsCount(rnd.Intn(300)))
}

// TestMaterializeSubtreeBounded_TreeNodeCeiling_Property asserts the
// invariant that must hold for every possible sibling-empty-dir count: when
// the tree's total node count is within MaxTreeNodes, materialization
// succeeds; when it exceeds MaxTreeNodes, MaterializeSubtreeBounded fails
// with ErrMaterializeBoundsExceeded — never a hang, a panic, or an
// unclassified error — for a tree that carries zero bytes and zero files
// (the exact shape MaxFiles/MaxTotalBytes cannot bound).
func TestMaterializeSubtreeBounded_TreeNodeCeiling_Property(t *testing.T) {
	t.Parallel()

	const maxTreeNodes = 50

	property := func(n manyEmptyDirsCount) bool {
		repoPath, sha := buildManyEmptyDirsRepo(t, int(n))
		src, err := gitsource.Open(repoPath)
		if err != nil {
			t.Fatalf("gitsource.Open: %v", err)
		}

		destDir := t.TempDir()
		bounds := gitsource.MaterializeBounds{MaxTotalBytes: 10 << 20, MaxFiles: 1000, MaxDepth: 50, MaxTreeNodes: maxTreeNodes}
		err = src.MaterializeSubtreeBounded(sha, "tenant", destDir, bounds)

		if int(n) > maxTreeNodes {
			if !errors.Is(err, gitsource.ErrMaterializeBoundsExceeded) {
				t.Logf("n=%d exceeds ceiling %d but error = %v, want ErrMaterializeBoundsExceeded", n, maxTreeNodes, err)
				return false
			}
			return true
		}

		if err != nil {
			t.Logf("n=%d within ceiling %d but got unexpected error: %v", n, maxTreeNodes, err)
			return false
		}
		return true
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 30}); err != nil {
		t.Error(err)
	}
}
