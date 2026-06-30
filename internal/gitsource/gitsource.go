// Package gitsource opens a local git repository and walks commits that touch
// a specific file path, yielding CommitSnapshot values in commit order
// (oldest first). It uses go-git (pure Go, no external git binary required).
//
// The high-water-mark SHA is used to resume incrementally: only commits *after*
// that SHA are returned. Pass an empty string to walk from the beginning.
//
// Remote HTTPS repos can be cloned/fetched with GitHub App installation tokens
// via OpenOrClone, which accepts an optional BasicAuth credential. Local paths
// continue to use PlainOpen unchanged.
package gitsource

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// maxCommitsPerWalk bounds how many commits a single walk loads into memory,
// guarding against unbounded memory use on a first-run backfill of a repo with
// long history. Commits are walked newest-first, so the most recent ones are
// kept. Hitting the cap is logged, never silent.
// TODO (backfill-and-poll-config task): replace this fixed cap with a
// per-tracker, config-driven backfill window.
const maxCommitsPerWalk = 5000

// Source wraps a local git repository for commit walking.
type Source struct {
	repo *git.Repository
}

// Open opens an existing local git repository at the given path.
func Open(path string) (*Source, error) {
	r, err := git.PlainOpen(path)
	if err != nil {
		return nil, fmt.Errorf("gitsource: open repo %q: %w", path, err)
	}
	return &Source{repo: r}, nil
}

// OpenOrClone opens an existing local repository at localPath if it exists,
// or clones from remoteURL into localPath if it does not. The auth parameter
// is passed to go-git for HTTPS basic authentication (use
// &gogithttp.BasicAuth{Username: "x-access-token", Password: token} for
// GitHub App installation tokens). Pass nil for unauthenticated access.
//
// This is the authenticated-remote entry point. For purely local fixture repos,
// the existing Open function continues to work as before.
func OpenOrClone(remoteURL, localPath string, auth gogithttp.AuthMethod) (*Source, error) {
	// If localPath already contains a git repo, open it.
	if isGitRepo(localPath) {
		r, err := git.PlainOpen(localPath)
		if err != nil {
			return nil, fmt.Errorf("gitsource: open existing clone at %q: %w", localPath, err)
		}
		return &Source{repo: r}, nil
	}

	// Clone from the remote URL into localPath.
	cloneOpts := &git.CloneOptions{
		URL:  remoteURL,
		Auth: auth,
	}
	r, err := git.PlainClone(localPath, false, cloneOpts)
	if err != nil {
		return nil, fmt.Errorf("gitsource: clone %q: %w", remoteURL, err)
	}
	return &Source{repo: r}, nil
}

// isGitRepo returns true if path exists and contains a .git directory (or is
// itself a bare repo). Used by OpenOrClone to detect an existing clone.
func isGitRepo(path string) bool {
	// Check for a .git directory (non-bare) or a HEAD file (bare).
	if info, err := os.Stat(filepath.Join(path, ".git")); err == nil && info.IsDir() {
		return true
	}
	if _, err := os.Stat(filepath.Join(path, "HEAD")); err == nil {
		return true
	}
	return false
}

// WalkCommits returns all commits that touched filePath, in chronological
// order (oldest first). If sinceCommitSha is non-empty, only commits strictly
// after that SHA are returned (used for incremental polling). If notBefore is
// non-zero, commits whose author-time is strictly before notBefore are excluded
// (used to bound the backfill window on first run). Pass a zero time.Time for
// notBefore to apply no lower time bound.
//
// The returned slice contains one CommitSnapshot per qualifying commit. The
// Content field holds the raw file bytes at that commit; if the file was
// deleted, Content is nil.
//
// This skeleton handles a single explicit file path. Glob expansion across many
// files (fan-out from a Tracker.FileGlob) is a seam left for the poller layer.
func (s *Source) WalkCommits(filePath, sinceCommitSha string, notBefore time.Time) ([]domain.CommitSnapshot, error) {
	head, err := s.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("gitsource: get HEAD: %w", err)
	}

	logOpts := &git.LogOptions{
		From:     head.Hash(),
		FileName: &filePath,
		Order:    git.LogOrderCommitterTime,
	}

	iter, err := s.repo.Log(logOpts)
	if err != nil {
		return nil, fmt.Errorf("gitsource: git log: %w", err)
	}
	defer iter.Close()

	// Collect all qualifying commits. git.Log returns newest first; we reverse
	// at the end to give the caller oldest-first ordering.
	var raw []domain.CommitSnapshot
	stopAt := plumbing.NewHash(sinceCommitSha)

	for {
		commit, err := iter.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("gitsource: iterate commits: %w", err)
		}

		// Stop at the high-water mark (exclusive — the HWM commit was already
		// processed in a prior run).
		if sinceCommitSha != "" && commit.Hash == stopAt {
			break
		}

		// Skip (stop walking) once we reach commits older than the backfill
		// window. The walk is newest-first, so once we cross the boundary all
		// subsequent commits are also out-of-window.
		if !notBefore.IsZero() && commit.Author.When.Before(notBefore) {
			break
		}

		content, err := fileContentAtCommit(commit, filePath)
		if err != nil {
			return nil, fmt.Errorf("gitsource: read file at %s: %w", commit.Hash, err)
		}

		raw = append(raw, domain.CommitSnapshot{
			CommitSha:   commit.Hash.String(),
			Author:      commit.Author.Name,
			CommittedAt: commit.Author.When,
			FilePath:    filePath,
			Content:     content,
		})

		if len(raw) >= maxCommitsPerWalk {
			log.Printf("gitsource: walk for %q hit the %d-commit cap; older history truncated", filePath, maxCommitsPerWalk)
			break
		}
	}

	// Reverse to chronological order (oldest first).
	for i, j := 0, len(raw)-1; i < j; i, j = i+1, j-1 {
		raw[i], raw[j] = raw[j], raw[i]
	}

	return raw, nil
}

// fileContentAtCommit extracts the raw bytes of filePath from the given commit's
// tree. Returns nil content (not an error) when the file doesn't exist in that
// commit (deleted).
func fileContentAtCommit(commit *object.Commit, filePath string) ([]byte, error) {
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("get tree: %w", err)
	}

	entry, err := tree.File(filePath)
	if err == object.ErrFileNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find file: %w", err)
	}

	contents, err := entry.Contents()
	if err != nil {
		return nil, fmt.Errorf("read contents: %w", err)
	}
	return []byte(contents), nil
}
