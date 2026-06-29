// Package gitsource opens a local git repository and walks commits that touch
// a specific file path, yielding CommitSnapshot values in commit order
// (oldest first). It uses go-git (pure Go, no external git binary required).
//
// The high-water-mark SHA is used to resume incrementally: only commits *after*
// that SHA are returned. Pass an empty string to walk from the beginning.
//
// TODO (github-app-auth task): add GitHub App token auth for remote repos.
// Currently only local repo paths are supported — sufficient for the skeleton
// and test fixture repos.
package gitsource

import (
	"fmt"
	"io"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

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

// WalkCommits returns all commits that touched filePath, in chronological
// order (oldest first). If sinceCommitSha is non-empty, only commits strictly
// after that SHA are returned (used for incremental polling).
//
// The returned slice contains one CommitSnapshot per qualifying commit. The
// Content field holds the raw file bytes at that commit; if the file was
// deleted, Content is nil.
//
// This skeleton handles a single explicit file path. Glob expansion across many
// files (fan-out from a Tracker.FileGlob) is a seam left for the poller layer.
func (s *Source) WalkCommits(filePath, sinceCommitSha string) ([]domain.CommitSnapshot, error) {
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

		// Skip commits at-or-before the high-water mark.
		if sinceCommitSha != "" && commit.Hash == stopAt {
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
