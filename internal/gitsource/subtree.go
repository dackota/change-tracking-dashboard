package gitsource

import (
	"errors"
	"fmt"
	"io"
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

	return materializeTree(s.repo.Storer, subtree, "", destDir)
}

// materializeTree recursively writes every blob under tree into destDir,
// building each file's destination path from relPrefix + the entry's own
// (untrusted) name. It walks tree.Entries directly rather than go-git's
// Tree.Files()/TreeWalker helpers, so securePath is the sole containment
// check exercised — not a side effect of an upstream library validation that
// could change or be absent in a different git backend.
func materializeTree(store storer.EncodedObjectStorer, tree *object.Tree, relPrefix, destDir string) error {
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
			if err := materializeTree(store, child, relPath, destDir); err != nil {
				return err
			}
		case isMaterializableFile(entry.Mode):
			if err := materializeBlob(store, entry, relPath, destDir); err != nil {
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
// validating containment first.
func materializeBlob(store storer.EncodedObjectStorer, entry object.TreeEntry, relPath, destDir string) error {
	dest, err := securePath(destDir, relPath)
	if err != nil {
		return fmt.Errorf("gitsource: materialize %q: %w", relPath, err)
	}

	blob, err := object.GetBlob(store, entry.Hash)
	if err != nil {
		return fmt.Errorf("gitsource: read blob %q: %w", relPath, err)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("gitsource: create parent dir for %q: %w", relPath, err)
	}

	if err := writeBlobToFile(blob, dest); err != nil {
		return fmt.Errorf("gitsource: write %q: %w", relPath, err)
	}

	return nil
}

// writeBlobToFile copies blob's raw bytes to dest via an io.Reader (never a
// string conversion, so binary content such as a vendored charts/*.tgz round
// trips byte-for-byte). Both Close calls are checked, not deferred-and-
// ignored: a write can fail silently at Close time (e.g. a flush error), and
// swallowing that would corrupt the binary-fidelity guarantee this function
// exists to provide.
func writeBlobToFile(blob *object.Blob, dest string) (err error) {
	r, err := blob.Reader()
	if err != nil {
		return fmt.Errorf("open blob reader: %w", err)
	}
	defer func() {
		if cerr := r.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close blob reader: %w", cerr)
		}
	}()

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create %q: %w", dest, err)
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close %q: %w", dest, cerr)
		}
	}()

	if _, err = io.Copy(out, r); err != nil {
		return fmt.Errorf("copy content: %w", err)
	}

	return nil
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
