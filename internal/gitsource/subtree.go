package gitsource

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// ErrNoParent indicates a commit has no parent — a root commit. Callers
// distinguish this from a real failure via errors.Is(err, gitsource.ErrNoParent)
// and should treat it as "no prior version to diff," not an error condition.
var ErrNoParent = errors.New("gitsource: commit has no parent (root commit)")

// ErrMaterializeBoundsExceeded indicates a MaterializeSubtreeBounded call
// aborted because the subtree being materialized exceeded one of the caller's
// configured ceilings (total bytes, file count, or recursion depth). Callers
// distinguish this from every other materialization failure (a real I/O
// error, a missing subtree) via errors.Is(err, gitsource.ErrMaterializeBoundsExceeded).
var ErrMaterializeBoundsExceeded = errors.New("gitsource: subtree materialization exceeded configured bounds")

// MaterializeBounds caps the resources MaterializeSubtreeBounded may spend
// materializing a single tenant chart subtree: the total bytes written
// across every file, the number of files written, and the tree's recursion
// depth. Untrusted repository content is otherwise unbounded on all three
// axes (an adversarial or corrupted tree could otherwise exhaust host disk
// or blow the goroutine stack via deep nesting), so a caller extracting
// untrusted content must always set real ceilings — MaterializeBounds has no
// "unbounded" zero-value meaning. Every check is enforced per-entry
// (materializeTree checks depth before descending into a child tree;
// materializeBlob checks file count and byte total before writing a file),
// not upfront against the whole subtree — so the zero value (all limits 0)
// rejects any subtree that actually contains at least one file or nested
// directory, but an empty subtree materializes trivially with no error
// (there is nothing to check against the ceiling).
type MaterializeBounds struct {
	// MaxTotalBytes is the maximum total bytes MaterializeSubtreeBounded may
	// write across every materialized file.
	MaxTotalBytes int64
	// MaxFiles is the maximum number of files MaterializeSubtreeBounded may
	// write.
	MaxFiles int
	// MaxDepth is the maximum tree recursion depth MaterializeSubtreeBounded
	// will descend. The subtree root is depth 0.
	MaxDepth int
}

// unboundedMaterializeBounds are the ceilings MaterializeSubtree (the
// pre-existing, unbounded entry point) applies internally: effectively no
// limit, so its documented behavior is unchanged by the addition of bounds
// enforcement at the shared materializeTree/materializeBlob chokepoint.
var unboundedMaterializeBounds = MaterializeBounds{
	MaxTotalBytes: math.MaxInt64,
	MaxFiles:      math.MaxInt,
	MaxDepth:      math.MaxInt,
}

// FirstParent resolves the first parent of the commit identified by
// commitSha, per ADR 0002: a merge commit's first parent is
// commit.ParentHashes[0] (the branch it was merged into); a root commit (no
// parents) returns ErrNoParent rather than a SHA.
func (s *Source) FirstParent(commitSha string) (string, error) {
	commit, err := s.resolveCommit(commitSha)
	if err != nil {
		return "", err
	}

	if len(commit.ParentHashes) == 0 {
		return "", ErrNoParent
	}

	return commit.ParentHashes[0].String(), nil
}

// resolveCommit looks up the commit object identified by commitSha.
func (s *Source) resolveCommit(commitSha string) (*object.Commit, error) {
	commit, err := s.repo.CommitObject(plumbing.NewHash(commitSha))
	if err != nil {
		return nil, fmt.Errorf("gitsource: get commit %q: %w", commitSha, err)
	}
	return commit, nil
}

// MaterializeSubtree writes every file under subtreePath (recursively, the
// entire tenant chart directory — every file, including vendored binary
// charts/*.tgz) as it existed at commitSha into destDir, so the result is a
// real on-disk directory chartrender.Render can load offline. File content is
// copied byte-for-byte from the git blob (via an io.Reader, never a string
// conversion), preserving binary fidelity. destDir must already exist; the
// caller owns its lifecycle (creation, bounding, and cleanup).
//
// subtreePath is the tenant chart directory — the directory containing the
// chart Change's Chart.yaml — relative to the repository root. If subtreePath
// does not exist as a tree at commitSha, MaterializeSubtree returns an error.
//
// Git tree entry names are untrusted, attacker-controlled repository
// content. MaterializeSubtree reads tree entries directly (not via go-git's
// higher-level tree-walking helpers) and validates every resolved
// destination path stays within destDir before writing, so this containment
// check — not an incidental upstream library guard — is the sole safeguard
// against a path-traversal or absolute-path tree entry (Zip-Slip class). A
// tree entry that would escape destDir causes MaterializeSubtree to return an
// error; no file is ever written outside destDir.
func (s *Source) MaterializeSubtree(commitSha, subtreePath, destDir string) error {
	return s.MaterializeSubtreeBounded(commitSha, subtreePath, destDir, unboundedMaterializeBounds)
}

// MaterializeSubtreeBounded behaves exactly like MaterializeSubtree, except
// it additionally enforces bounds on total bytes written, file count, and
// recursion depth — closing the DoS a hostile or corrupted repository could
// otherwise mount against MaterializeSubtree's unbounded recursive walk (an
// arbitrarily large or deeply nested tenant subtree would otherwise exhaust
// host disk or the goroutine stack).
//
// The check is enforced at the single chokepoint every materialization call
// funnels through (materializeTree/materializeBlob below), not case-by-case:
// MaterializeSubtree itself is now a thin wrapper calling this function with
// effectively unbounded limits, so both entry points share one enforcement
// path and can never disagree about containment or bounds.
//
// If bounds.MaxTotalBytes, MaxFiles, or MaxDepth would be exceeded,
// MaterializeSubtreeBounded returns an error satisfying
// errors.Is(err, ErrMaterializeBoundsExceeded) and stops immediately: no
// file that would push the running total over MaxTotalBytes is ever written
// (the ceiling is checked against each blob's declared size before any of
// its bytes are copied), so destDir never grows past the configured ceiling
// because of this call.
func (s *Source) MaterializeSubtreeBounded(commitSha, subtreePath, destDir string, bounds MaterializeBounds) error {
	commit, err := s.resolveCommit(commitSha)
	if err != nil {
		return err
	}

	tree, err := commit.Tree()
	if err != nil {
		return fmt.Errorf("gitsource: get tree for commit %q: %w", commitSha, err)
	}

	subtree, err := tree.Tree(subtreePath)
	if err != nil {
		return fmt.Errorf("gitsource: subtree %q not found at commit %q: %w", subtreePath, commitSha, err)
	}

	state := &materializeState{bounds: bounds}
	return materializeTree(s.repo.Storer, subtree, "", destDir, 0, state)
}

// materializeState tracks resource usage across an entire
// MaterializeSubtreeBounded call — shared by pointer across every recursive
// materializeTree/materializeBlob invocation in that call, so bytesWritten
// and filesWritten are running totals over the whole subtree, not per-directory
// counts.
type materializeState struct {
	bounds       MaterializeBounds
	bytesWritten int64
	filesWritten int
}

// materializeTree recursively writes every blob under tree into destDir,
// building each file's destination path from relPrefix + the entry's own
// (untrusted) name. It walks tree.Entries directly rather than go-git's
// Tree.Files()/TreeWalker helpers, so securePath is the sole containment
// check exercised — not a side effect of an upstream library validation that
// could change or be absent in a different git backend.
//
// depth is the current recursion depth (the subtree root passed to
// MaterializeSubtreeBounded is depth 0); state carries the bounds ceiling and
// the running byte/file counters shared across the whole call.
func materializeTree(store storer.EncodedObjectStorer, tree *object.Tree, relPrefix, destDir string, depth int, state *materializeState) error {
	if depth > state.bounds.MaxDepth {
		return fmt.Errorf("gitsource: tree %q at depth %d: %w (max depth %d)", relPrefix, depth, ErrMaterializeBoundsExceeded, state.bounds.MaxDepth)
	}

	for _, entry := range tree.Entries {
		relPath := entry.Name
		if relPrefix != "" {
			relPath = relPrefix + "/" + entry.Name
		}

		switch {
		case entry.Mode == filemode.Dir:
			child, err := object.GetTree(store, entry.Hash)
			if err != nil {
				return fmt.Errorf("gitsource: read subtree %q: %w", relPath, err)
			}
			if err := materializeTree(store, child, relPath, destDir, depth+1, state); err != nil {
				return err
			}
		case isMaterializableFile(entry.Mode):
			if err := materializeBlob(store, entry, relPath, destDir, state); err != nil {
				return err
			}
		default:
			// Symlinks and submodules are skipped: a symlink blob's content is
			// a target path string, not real file bytes, and neither occurs
			// in a vendored Helm chart subtree.
			continue
		}
	}
	return nil
}

// isMaterializableFile reports whether mode is a regular or executable file
// — the only tree entry kinds MaterializeSubtree writes as file content.
func isMaterializableFile(mode filemode.FileMode) bool {
	return mode == filemode.Regular || mode == filemode.Deprecated || mode == filemode.Executable
}

// materializeBlob writes a single blob's content to destDir/relPath,
// validating containment and the configured bounds first. The file-count and
// total-byte ceilings are both checked against the blob's declared Size
// before any byte of this blob is copied: a blob whose declared size would
// push state.bytesWritten past state.bounds.MaxTotalBytes is never opened
// for writing at all, so a single oversized file can't partially land on
// disk before being rejected. The actual copy (writeBlobToFile) then
// re-enforces the same remaining-budget ceiling against the bytes it
// actually copies, not just the blob's declared Size — defense-in-depth
// against a size-lying or corrupted blob, so the on-disk ceiling never
// depends solely on trusting that declared value.
func materializeBlob(store storer.EncodedObjectStorer, entry object.TreeEntry, relPath, destDir string, state *materializeState) error {
	dest, err := securePath(destDir, relPath)
	if err != nil {
		return fmt.Errorf("gitsource: materialize %q: %w", relPath, err)
	}

	blob, err := object.GetBlob(store, entry.Hash)
	if err != nil {
		return fmt.Errorf("gitsource: read blob %q: %w", relPath, err)
	}

	if state.filesWritten+1 > state.bounds.MaxFiles {
		return fmt.Errorf("gitsource: materialize %q: %w (max files %d)", relPath, ErrMaterializeBoundsExceeded, state.bounds.MaxFiles)
	}
	if state.bytesWritten+blob.Size > state.bounds.MaxTotalBytes {
		return fmt.Errorf("gitsource: materialize %q: %w (max total bytes %d)", relPath, ErrMaterializeBoundsExceeded, state.bounds.MaxTotalBytes)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("gitsource: create parent dir for %q: %w", relPath, err)
	}

	remaining := state.bounds.MaxTotalBytes - state.bytesWritten
	written, err := writeBlobToFile(blob, dest, remaining)
	if err != nil {
		if errors.Is(err, ErrMaterializeBoundsExceeded) {
			return fmt.Errorf("gitsource: materialize %q: %w (max total bytes %d)", relPath, ErrMaterializeBoundsExceeded, state.bounds.MaxTotalBytes)
		}
		return fmt.Errorf("gitsource: write %q: %w", relPath, err)
	}

	state.filesWritten++
	state.bytesWritten += written

	return nil
}

// writeBlobToFile copies blob's raw bytes to dest via an io.Reader (never a
// string conversion, so binary content such as a vendored charts/*.tgz round
// trips byte-for-byte), never writing more than maxBytes regardless of how
// much content the blob actually holds. It delegates the copy itself to
// copyBounded — the single chokepoint that ties the on-disk byte ceiling to
// bytes actually copied, not to the blob's self-reported (and, in principle,
// forgeable or corrupted) Size.
func writeBlobToFile(blob *object.Blob, dest string, maxBytes int64) (written int64, err error) {
	r, err := blob.Reader()
	if err != nil {
		return 0, fmt.Errorf("open blob reader: %w", err)
	}
	defer func() {
		if cerr := r.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close blob reader: %w", cerr)
		}
	}()

	return copyBounded(r, dest, maxBytes)
}

// copyBounded copies from r to a freshly created file at dest, writing at
// most maxBytes. If r still has unread data once maxBytes has been copied,
// copyBounded returns ErrMaterializeBoundsExceeded — the ceiling this
// enforces holds against the actual byte stream, so it can never be defeated
// by a source whose declared size understates what it actually produces.
// Both Close calls are checked, not deferred-and-ignored: a write can fail
// silently at Close time (e.g. a flush error), and swallowing that would
// corrupt the binary-fidelity guarantee writeBlobToFile exists to provide.
func copyBounded(r io.Reader, dest string, maxBytes int64) (written int64, err error) {
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, fmt.Errorf("create %q: %w", dest, err)
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close %q: %w", dest, cerr)
		}
	}()

	written, copyErr := io.CopyN(out, r, maxBytes)
	if copyErr != nil && !errors.Is(copyErr, io.EOF) {
		return written, fmt.Errorf("copy content: %w", copyErr)
	}
	if copyErr == nil {
		// io.CopyN copied exactly maxBytes with no error, which leaves it
		// ambiguous whether r had exactly maxBytes or more left unread —
		// probe for one more byte to tell those apart without ever writing
		// it to dest.
		var probe [1]byte
		if n, _ := r.Read(probe[:]); n > 0 {
			return written, ErrMaterializeBoundsExceeded
		}
	}
	return written, nil
}

// securePath resolves relPath (an untrusted git tree entry name, possibly
// attacker-controlled) against destDir and returns the joined path only if
// it stays within destDir. It rejects any relPath that would resolve outside
// destDir via ".." traversal or an absolute-path component (the Zip-Slip
// class of vulnerability), using filepath.Rel to determine containment
// rather than a plain string-prefix check on the unresolved input.
func securePath(destDir, relPath string) (string, error) {
	cleanDest := filepath.Clean(destDir)
	joined := filepath.Join(cleanDest, filepath.FromSlash(relPath))

	rel, err := filepath.Rel(cleanDest, joined)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes destination directory", relPath)
	}

	return joined, nil
}
