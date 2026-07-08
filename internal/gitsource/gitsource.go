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
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
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

// Fetch performs an authenticated git fetch for the given remoteURL, updating
// the local clone's remote-tracking refs and fast-forwarding the local branch so
// that WalkCommits (which walks from repo.Head()) observes any commits pushed to
// the remote after the initial clone.
//
// If remoteURL is empty, Fetch is a no-op — local-path Sources opened via Open
// have no remote and are never fetched.
//
// git.NoErrAlreadyUpToDate is treated as success: there is nothing new to fetch
// on this cycle, which is the common steady-state case.
//
// The auth parameter must not appear in any error message (never log tokens).
func (s *Source) Fetch(remoteURL string, auth gogithttp.AuthMethod) error {
	if remoteURL == "" {
		return nil
	}

	fetchOpts := &git.FetchOptions{
		RemoteURL: remoteURL,
		RefSpecs:  []config.RefSpec{"+refs/heads/*:refs/remotes/origin/*"},
		Auth:      auth,
		Force:     true,
	}

	err := s.repo.Fetch(fetchOpts)
	if err != nil && err != git.NoErrAlreadyUpToDate {
		// Return a fixed non-leaking error: transport errors can embed the remote's
		// HTTP response body (which may contain auth challenges or server details).
		// We discard the underlying error entirely — mirroring how
		// githubapp/provider.go returns "token endpoint request failed" with no %w.
		return fmt.Errorf("gitsource: fetch from remote failed")
	}

	// Fast-forward the local branch to the fetched remote-tracking ref so that
	// s.repo.Head() — and therefore WalkCommits — sees the newly fetched commits.
	// go-git's Fetch updates refs/remotes/origin/<branch> but does NOT move the
	// local branch ref; we must do that explicitly.
	return s.fastForwardToRemote()
}

// fastForwardToRemote resolves the remote-tracking ref that corresponds to the
// current HEAD branch and updates the local branch ref to match it.
//
// Example: if HEAD → refs/heads/main, we look up refs/remotes/origin/main and
// set refs/heads/main to that hash.
func (s *Source) fastForwardToRemote() error {
	head, err := s.repo.Head()
	if err != nil {
		return fmt.Errorf("gitsource: resolve HEAD for fast-forward: %w", err)
	}

	// Only fast-forward when HEAD is on a named branch (not detached).
	if !head.Name().IsBranch() {
		return nil
	}

	// Short branch name, e.g. "main".
	branchName := head.Name().Short()

	// Look up the remote-tracking ref, e.g. refs/remotes/origin/main.
	remoteRefName := plumbing.NewRemoteReferenceName("origin", branchName)
	remoteRef, err := s.repo.Reference(remoteRefName, true)
	if err != nil {
		// The remote-tracking ref may not exist (e.g. the remote uses a
		// different default branch name). Treat as a non-fatal condition.
		return nil
	}

	// Update the local branch ref to the remote-tracking hash.
	localRef := plumbing.NewHashReference(head.Name(), remoteRef.Hash())
	if err := s.repo.Storer.SetReference(localRef); err != nil {
		return fmt.Errorf("gitsource: fast-forward local branch: %w", err)
	}

	return nil
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
// ctx is used only for log correlation: the commit-cap warning below is
// logged via telemetry.LoggerFromContext(ctx) so it carries the trace_id/
// span_id of the poll cycle that's walking commits (poller.go stores its
// poll-scoped logger on ctx via telemetry.ContextWithLogger before calling
// down into this package). Passing context.Background() (as every
// pre-existing caller in this package's tests does) simply yields the
// uncorrelated package-default logger — never a crash or nil.
//
// The returned slice contains one CommitSnapshot per qualifying commit. The
// Content field holds the raw file bytes at that commit; if the file was
// deleted, Content is nil.
//
// This skeleton handles a single explicit file path. Glob expansion across many
// files (fan-out from a Tracker.FileGlob) is a seam left for the poller layer.
func (s *Source) WalkCommits(ctx context.Context, filePath, sinceCommitSha string, notBefore time.Time) ([]domain.CommitSnapshot, error) {
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
			Message:     commit.Message,
		})

		if len(raw) >= maxCommitsPerWalk {
			warnCommitCapHit(ctx, filePath)
			break
		}
	}

	// Reverse to chronological order (oldest first).
	for i, j := 0, len(raw)-1; i < j; i, j = i+1, j-1 {
		raw[i], raw[j] = raw[j], raw[i]
	}

	return raw, nil
}

// warnCommitCapHit logs the commit-cap truncation warning, correlated to
// ctx's active trace when WalkCommits was called with one (see WalkCommits'
// doc comment on ctx). Split out from WalkCommits as its own named seam so
// the log/trace-correlation behavior can be asserted directly, without
// needing to actually walk maxCommitsPerWalk real commits in a test.
func warnCommitCapHit(ctx context.Context, filePath string) {
	telemetry.LoggerFromContext(ctx).Warn("gitsource: walk hit the commit cap; older history truncated",
		"filePath", filePath, "cap", maxCommitsPerWalk)
}

// MatchingFiles enumerates every blob path in the repository's HEAD tree and
// returns those whose path matches glob. A single "*" matches any sequence of
// non-separator characters within one path segment; "?" matches a single
// non-separator character; and "**" is a cross-segment wildcard matching any
// number of path segments (so "gitops/**/Chart.yaml" matches
// "gitops/a/Chart.yaml", "gitops/a/b/Chart.yaml", and — via the "**/" form —
// "gitops/Chart.yaml"). Paths are git-style forward-slash-separated.
//
// Globs containing "**" are compiled to a regexp (globToRegexp); globs without
// it keep exact path.Match semantics (preserving "[...]" character-class
// support). The returned slice is sorted lexicographically for deterministic
// fan-out ordering. Matching is scoped to files present at HEAD — a file
// deleted before HEAD is not discovered, mirroring how a literal FileGlob only
// ever tracked a file that currently exists.
func (s *Source) MatchingFiles(glob string) ([]string, error) {
	head, err := s.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("gitsource: get HEAD: %w", err)
	}

	commit, err := s.repo.CommitObject(head.Hash())
	if err != nil {
		return nil, fmt.Errorf("gitsource: get HEAD commit: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("gitsource: get HEAD tree: %w", err)
	}

	// Globs with a "**" cross-segment wildcard are compiled to a regexp once;
	// simpler globs keep path.Match semantics so "[...]" classes still work.
	var re *regexp.Regexp
	if strings.Contains(glob, "**") {
		re, err = globToRegexp(glob)
		if err != nil {
			return nil, fmt.Errorf("gitsource: compile glob %q: %w", glob, err)
		}
	}

	var matches []string
	walker := tree.Files()
	defer walker.Close()
	for {
		f, err := walker.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("gitsource: walk HEAD tree: %w", err)
		}

		var ok bool
		if re != nil {
			ok = re.MatchString(f.Name)
		} else {
			ok, err = path.Match(glob, f.Name)
			if err != nil {
				return nil, fmt.Errorf("gitsource: match glob %q: %w", glob, err)
			}
		}
		if ok {
			matches = append(matches, f.Name)
		}
	}

	sort.Strings(matches)
	return matches, nil
}

// globToRegexp translates a path glob that may contain the "**" cross-segment
// wildcard into an anchored regexp over forward-slash-separated paths:
//
//	**/  → (?:.*/)?   any number of leading path segments (incl. none)
//	**   → .*         any characters, crossing separators
//	*    → [^/]*      any characters within a single segment
//	?    → [^/]       one character within a single segment
//
// Every other character is escaped as a literal, so a glob never smuggles in
// regexp metacharacters. Used only for globs containing "**"; simpler globs
// stay on path.Match (see MatchingFiles).
func globToRegexp(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteByte('^')
	for i := 0; i < len(glob); {
		c := glob[i]
		switch c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i += 2
				if i < len(glob) && glob[i] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
				continue
			}
			b.WriteString("[^/]*")
			i++
		case '?':
			b.WriteString("[^/]")
			i++
		case '[':
			// Character class — copy through to RE2 verbatim (path.Match and RE2
			// share the syntax: "^" negation, ranges "lo-hi", and a "]" right
			// after "[" / "[^" is a literal member). This keeps class semantics
			// (e.g. "gitops/**/values-[a-z].yaml") instead of escaping them into
			// a literal. Inside the class, "*"/"?" are literal, which is correct.
			j := i + 1
			if j < len(glob) && glob[j] == '^' {
				j++
			}
			if j < len(glob) && glob[j] == ']' { // literal "]" as first member
				j++
			}
			for j < len(glob) && glob[j] != ']' {
				j++
			}
			if j >= len(glob) {
				// Unterminated "[" — emit a literal "[" (a bare "[" is invalid in
				// RE2); mirrors path.Match rejecting the pattern.
				b.WriteString(`\[`)
				i++
				continue
			}
			b.WriteString(glob[i : j+1]) // include the closing "]"
			i = j + 1
		default:
			if strings.IndexByte(`.+()|]{}^$\`, c) >= 0 {
				b.WriteByte('\\')
			}
			b.WriteByte(c)
			i++
		}
	}
	b.WriteByte('$')
	return regexp.Compile(b.String())
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
