package gitsource_test

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"testing/quick"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// buildTenantChartRepo creates a temporary git repo with a single commit that
// adds every file in files (keyed by path relative to the tenant chart
// directory "tenant/") under "tenant/", then commits. Returns the repo path
// and the commit SHA. File content is written with os.WriteFile, which is
// binary-safe (no newline translation), so callers can seed arbitrary bytes.
func buildTenantChartRepo(t *testing.T, files map[string][]byte) (repoPath, sha string) {
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

	// Deterministic add order for reproducible commits.
	relPaths := make([]string, 0, len(files))
	for rel := range files {
		relPaths = append(relPaths, rel)
	}
	sort.Strings(relPaths)

	for _, rel := range relPaths {
		full := filepath.Join(dir, "tenant", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %q: %v", rel, err)
		}
		if err := os.WriteFile(full, files[rel], 0o644); err != nil {
			t.Fatalf("write %q: %v", rel, err)
		}
		if _, err := wt.Add(filepath.ToSlash(filepath.Join("tenant", rel))); err != nil {
			t.Fatalf("git add %q: %v", rel, err)
		}
	}

	c, err := wt.Commit("chore: seed tenant chart", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "tester",
			Email: "tester@example.com",
			When:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	return dir, c.String()
}

// walkMaterializedFiles returns the set of file paths (relative to root,
// forward-slash separated) written under root, for comparing against the
// expected blob path set.
func walkMaterializedFiles(t *testing.T, root string) map[string]bool {
	t.Helper()

	got := make(map[string]bool)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		got[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk materialized dir: %v", err)
	}
	return got
}

// TestMaterializeSubtree_BinaryByteFidelity_Property asserts the round-trip
// invariant that must hold for every possible blob content: the materialized
// file's bytes are byte-for-byte identical to what was committed, for
// arbitrary binary content (including NUL, high bytes, and \r\n sequences
// that a text-mode write would otherwise mangle).
func TestMaterializeSubtree_BinaryByteFidelity_Property(t *testing.T) {
	t.Parallel()

	property := func(content []byte) bool {
		repoPath, sha := buildTenantChartRepo(t, map[string][]byte{
			"Chart.yaml":     []byte("apiVersion: v2\nname: tenant\nversion: 1.0.0\n"),
			"charts/dep.tgz": content,
		})

		src, err := gitsource.Open(repoPath)
		if err != nil {
			t.Fatalf("gitsource.Open: %v", err)
		}

		destDir := t.TempDir()
		if err := src.MaterializeSubtree(sha, "tenant", destDir); err != nil {
			t.Fatalf("MaterializeSubtree: %v", err)
		}

		got, err := os.ReadFile(filepath.Join(destDir, "charts", "dep.tgz"))
		if err != nil {
			t.Fatalf("read materialized blob: %v", err)
		}

		return bytes.Equal(got, content)
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 30}); err != nil {
		t.Error(err)
	}
}

// fileSet is a randomized set of distinct relative file paths under the
// tenant chart directory, used to drive the completeness property test.
// Each entry bakes its own index into its name, so generated paths can
// never collide or nest as a prefix of one another regardless of the random
// top-level/nested choice made — every generated set is a valid,
// unambiguous git tree shape.
type fileSet []string

// Generate implements quick.Generator, producing between 0 and 6 distinct
// relative paths, each either top-level ("fileN.bin") or one directory deep
// ("dirN/fileN.bin").
func (fileSet) Generate(rnd *rand.Rand, size int) reflect.Value {
	n := rnd.Intn(7)
	paths := make(fileSet, 0, n)
	for i := 0; i < n; i++ {
		if rnd.Intn(2) == 0 {
			paths = append(paths, fmt.Sprintf("file%d.bin", i))
		} else {
			paths = append(paths, fmt.Sprintf("dir%d/file%d.bin", i, i))
		}
	}
	return reflect.ValueOf(paths)
}

// TestMaterializeSubtree_Completeness_Property asserts the invariant that
// the set of materialized file paths (relative to destDir) is exactly the
// set of blob paths under the subtree prefix at that commit — nothing
// missing (every committed file must be written) and nothing extra (no
// stray or duplicated file appears), for an arbitrary randomized file
// layout rather than one hand-picked tree shape.
func TestMaterializeSubtree_Completeness_Property(t *testing.T) {
	t.Parallel()

	property := func(paths fileSet) bool {
		files := map[string][]byte{
			"Chart.yaml": []byte("apiVersion: v2\nname: tenant\nversion: 1.0.0\n"),
		}
		want := map[string]bool{"Chart.yaml": true}
		for i, p := range paths {
			files[p] = []byte(fmt.Sprintf("content-%d", i))
			want[p] = true
		}

		repoPath, sha := buildTenantChartRepo(t, files)
		src, err := gitsource.Open(repoPath)
		if err != nil {
			t.Fatalf("gitsource.Open: %v", err)
		}

		destDir := t.TempDir()
		if err := src.MaterializeSubtree(sha, "tenant", destDir); err != nil {
			t.Fatalf("MaterializeSubtree: %v", err)
		}

		got := walkMaterializedFiles(t, destDir)
		return reflect.DeepEqual(got, want)
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 30}); err != nil {
		t.Error(err)
	}
}

// buildMaliciousSubtreeRepo builds — via raw plumbing object writes, bypassing
// go-git's worktree Add/Commit flow entirely — a repo whose "tenant" subtree
// contains one legitimate file (Chart.yaml) and one nested tree ("evil")
// whose own entry has the traversal name "../../../escaped.txt". This is the
// shape a malicious or corrupted repository could present: tree entry names
// are untrusted, attacker-controlled content, not something MaterializeSubtree
// can assume is well-formed. Returns the repo path and commit SHA.
func buildMaliciousSubtreeRepo(t *testing.T) (repoPath, sha string) {
	t.Helper()

	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}
	store := repo.Storer

	writeBlob := func(content string) plumbing.Hash {
		obj := store.NewEncodedObject()
		obj.SetType(plumbing.BlobObject)
		w, err := obj.Writer()
		if err != nil {
			t.Fatalf("blob writer: %v", err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("blob write: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("blob writer close: %v", err)
		}
		h, err := store.SetEncodedObject(obj)
		if err != nil {
			t.Fatalf("store blob: %v", err)
		}
		return h
	}

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

	chartBlob := writeBlob("apiVersion: v2\nname: tenant\nversion: 1.0.0\n")
	evilBlob := writeBlob("this content must never land outside destDir")

	evilTreeHash := writeTree([]object.TreeEntry{
		{Name: "../../../escaped.txt", Mode: filemode.Regular, Hash: evilBlob},
	})

	tenantTreeHash := writeTree([]object.TreeEntry{
		{Name: "Chart.yaml", Mode: filemode.Regular, Hash: chartBlob},
		{Name: "evil", Mode: filemode.Dir, Hash: evilTreeHash},
	})

	rootTreeHash := writeTree([]object.TreeEntry{
		{Name: "tenant", Mode: filemode.Dir, Hash: tenantTreeHash},
	})

	when := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	commit := &object.Commit{
		Author:    object.Signature{Name: "attacker", Email: "attacker@example.com", When: when},
		Committer: object.Signature{Name: "attacker", Email: "attacker@example.com", When: when},
		Message:   "malicious tree entry",
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

// TestMaterializeSubtree_MaliciousTreeEntry_NeverWritesOutsideDestDir is the
// end-to-end security test: given a real (if unusually constructed) git tree
// containing a path-traversal entry name, MaterializeSubtree must reject it
// with an error and must never create the file it attempted to escape to,
// proving the containment check is enforced on the actual production code
// path — not only against the pure securePath helper in isolation.
func TestMaterializeSubtree_MaliciousTreeEntry_NeverWritesOutsideDestDir(t *testing.T) {
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

	err = src.MaterializeSubtree(sha, "tenant", destDir)
	if err == nil {
		t.Fatal("MaterializeSubtree with a path-traversal tree entry returned nil error, want rejection")
	}

	// The malicious entry's name ("../../../escaped.txt", relative to
	// "evil/") targets three levels above destDir. Confirm nothing was
	// written there.
	escapedTarget := filepath.Join(base, "..", "..", "escaped.txt")
	if _, statErr := os.Stat(escapedTarget); !os.IsNotExist(statErr) {
		t.Fatalf("escaped file was written outside destDir at %q (stat err: %v)", escapedTarget, statErr)
	}

	// Nothing under destDir should exist for the malicious "evil" subtree
	// either — the failure must have occurred before any of its content
	// (legitimate-looking or not) was written.
	if _, statErr := os.Stat(filepath.Join(destDir, "evil")); !os.IsNotExist(statErr) {
		t.Errorf("materialized 'evil' entry despite the containment error: %v", statErr)
	}
}

// TestMaterializeSubtree_ExtractsChartIncludingBinaryTgz is the PRD's
// headline example test: extracting a tenant chart subtree that vendors a
// binary charts/*.tgz at a commit yields every file, with the vendored
// artifact byte-identical to what was committed.
func TestMaterializeSubtree_ExtractsChartIncludingBinaryTgz(t *testing.T) {
	t.Parallel()

	// A "binary-looking" payload: not valid UTF-8, includes NUL and \r\n —
	// stands in for a real gzip/tar charts/*.tgz artifact.
	tgzBytes := []byte{0x1f, 0x8b, 0x08, 0x00, 0x00, 0x0d, 0x0a, 0x00, 0xff, 0xfe, 0x00, 0x01, 0x02, 0x03}

	files := map[string][]byte{
		"Chart.yaml":     []byte("apiVersion: v2\nname: tenant\nversion: 1.0.0\ndependencies:\n  - name: dep\n    version: 0.1.0\n"),
		"values.yaml":    []byte("replicaCount: 1\n"),
		"charts/dep.tgz": tgzBytes,
	}

	repoPath, sha := buildTenantChartRepo(t, files)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	destDir := t.TempDir()
	if err := src.MaterializeSubtree(sha, "tenant", destDir); err != nil {
		t.Fatalf("MaterializeSubtree: %v", err)
	}

	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(destDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read materialized %q: %v", rel, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("materialized %q = %v, want %v (byte-identical to git blob)", rel, got, want)
		}
	}
}

// TestMaterializeSubtree_SubtreeNotPresentAtCommit_ReturnsError verifies that
// requesting a tenant chart directory that does not exist at the given
// commit returns a real error, not a silently empty result.
func TestMaterializeSubtree_SubtreeNotPresentAtCommit_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath, sha := buildTenantChartRepo(t, map[string][]byte{
		"Chart.yaml": []byte("apiVersion: v2\nname: tenant\nversion: 1.0.0\n"),
	})

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	destDir := t.TempDir()
	err = src.MaterializeSubtree(sha, "does-not-exist", destDir)
	if err == nil {
		t.Fatal("MaterializeSubtree with a nonexistent subtree path returned nil error, want an error")
	}
}

// TestFirstParent_TwoCommitRepo verifies that the child commit's first
// parent resolves to the earlier commit's SHA.
func TestFirstParent_TwoCommitRepo(t *testing.T) {
	t.Parallel()

	repoPath, sha1, sha2 := buildFixtureRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	parent, err := src.FirstParent(sha2)
	if err != nil {
		t.Fatalf("FirstParent(sha2): %v", err)
	}
	if parent != sha1 {
		t.Errorf("FirstParent(sha2) = %q, want sha1 = %q", parent, sha1)
	}
}

// TestFirstParent_ThreeCommitRepo verifies first-parent resolution across a
// three-commit linear chain: each commit's first parent is its immediate
// predecessor.
func TestFirstParent_ThreeCommitRepo(t *testing.T) {
	t.Parallel()

	repoPath, sha1, sha2, sha3 := buildThreeCommitRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	parentOf3, err := src.FirstParent(sha3)
	if err != nil {
		t.Fatalf("FirstParent(sha3): %v", err)
	}
	if parentOf3 != sha2 {
		t.Errorf("FirstParent(sha3) = %q, want sha2 = %q", parentOf3, sha2)
	}

	parentOf2, err := src.FirstParent(sha2)
	if err != nil {
		t.Fatalf("FirstParent(sha2): %v", err)
	}
	if parentOf2 != sha1 {
		t.Errorf("FirstParent(sha2) = %q, want sha1 = %q", parentOf2, sha1)
	}
}

// TestFirstParent_RootCommit_ReturnsErrNoParent verifies that a root commit
// (no parents) is reported via the ErrNoParent sentinel — a normal,
// distinguishable-from-a-real-error "no prior version to diff" condition,
// checked with errors.Is per the documented contract.
func TestFirstParent_RootCommit_ReturnsErrNoParent(t *testing.T) {
	t.Parallel()

	repoPath, sha1, _ := buildFixtureRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	_, err = src.FirstParent(sha1)
	if err == nil {
		t.Fatal("FirstParent(root commit) returned nil error, want ErrNoParent")
	}
	if !errors.Is(err, gitsource.ErrNoParent) {
		t.Errorf("FirstParent(root commit) error = %v, want errors.Is(err, gitsource.ErrNoParent) == true", err)
	}
}

// TestFirstParent_MergeCommit_ResolvesToFirstParentOnly builds a merge commit
// with two parents (via explicit CommitOptions.Parents, mirroring how a real
// merge commit records both branches) and verifies FirstParent returns only
// ParentHashes[0] — the branch the merge commit was merged into — not the
// second parent.
func TestFirstParent_MergeCommit_ResolvesToFirstParentOnly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}

	chartPath := filepath.Join(dir, "Chart.yaml")
	commitFile := func(content, msg string, when time.Time, parents []plumbing.Hash) plumbing.Hash {
		if err := os.WriteFile(chartPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := wt.Add("Chart.yaml"); err != nil {
			t.Fatalf("add: %v", err)
		}
		h, err := wt.Commit(msg, &git.CommitOptions{
			Author:  &object.Signature{Name: "dev", Email: "d@x.com", When: when},
			Parents: parents,
		})
		if err != nil {
			t.Fatalf("commit %q: %v", msg, err)
		}
		return h
	}

	root := commitFile("version: \"1.0.0\"\n", "root", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), nil)
	branchA := commitFile("version: \"1.1.0\"\n", "branch-a", time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), []plumbing.Hash{root})
	branchB := commitFile("version: \"1.2.0\"\n", "branch-b", time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC), []plumbing.Hash{root})
	merge := commitFile("version: \"1.3.0\"\n", "merge", time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC), []plumbing.Hash{branchA, branchB})

	src, err := gitsource.Open(dir)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	parent, err := src.FirstParent(merge.String())
	if err != nil {
		t.Fatalf("FirstParent(merge): %v", err)
	}
	if parent != branchA.String() {
		t.Errorf("FirstParent(merge) = %q, want branchA (first parent) = %q, not branchB = %q", parent, branchA.String(), branchB.String())
	}
}
