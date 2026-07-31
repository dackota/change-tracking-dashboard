// Package reposource keeps one working copy per tracked repo and hands it out
// on demand. A tracked repo is named by a local path or an https:// URL; this
// package is what turns that name into an open gitsource.Source, cloning it
// the first time and fetching it on subsequent requests so a poll cycle sees
// commits pushed since the last one.
//
// It is the only place that decides whether credentials are attached to a git
// operation, and the only place that knows where a remote repo is cloned to on
// disk. Nothing else in the process opens a tracked repo.
package reposource

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/subtree"
	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// TokenProvider mints a short-lived token for authenticating to a remote.
// It is satisfied by *githubapp.Provider; this package declares the one method
// it needs rather than importing that concrete type, so a Cache can be tested
// without a GitHub App.
type TokenProvider interface {
	Token() (string, error)
}

// Cache maps a tracked repo's name (local path or https:// URL) to its open
// working copy.
//
// Opening a local repo is cheap, but re-opening one on every poll cycle is
// wasted work, and re-cloning a remote one would be ruinous — so a Source is
// opened once and kept. A remote-backed Source is fetched on every Get, which
// is how commits pushed since the last cycle become visible: the Source
// remembers its own remote, so the Cache does not carry the URL alongside it.
//
// Clone directories live under os.TempDir(), typically tmpfs on a Kubernetes
// node, so they are ephemeral: a pod restart re-clones from scratch. That is
// intentional — the store's high-water marks resume incremental polling after
// a re-clone with no data loss.
type Cache struct {
	// mu guards sources and is held across the fetch inside Get. That
	// serializes concurrent Gets — including ones for unrelated repos —
	// behind a network round trip. Acceptable while polling is the only
	// caller and the scheduler drives it one group at a time; it is the first
	// thing to revisit if on-demand diff requests start contending here.
	mu      sync.Mutex
	sources map[string]*gitsource.Source

	tokens TokenProvider // nil when token auth is disabled

	// cloneRoot is the directory remote clones are placed under. Tests point
	// it at a temp dir; production leaves it empty, which means os.TempDir().
	cloneRoot string
}

// Option configures a Cache.
type Option func(*Cache)

// WithTokenProvider attaches a TokenProvider, enabling authenticated access to
// remote https:// repos. Without it, remotes are accessed unauthenticated.
func WithTokenProvider(tp TokenProvider) Option {
	return func(c *Cache) { c.tokens = tp }
}

// WithCloneRoot overrides the directory remote clones are written under.
// Defaults to os.TempDir().
func WithCloneRoot(dir string) Option {
	return func(c *Cache) { c.cloneRoot = dir }
}

// New returns an empty Cache.
func New(opts ...Option) *Cache {
	c := &Cache{sources: make(map[string]*gitsource.Source)}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Get returns the working copy for the given repo, opening or cloning it on
// the first call. On every subsequent call a remote-backed Source is fetched
// first, so commits pushed since the last call are visible to the caller that
// is about to walk its history. A local-path Source has no remote and is
// returned as-is.
func (c *Cache) Get(repo string) (*gitsource.Source, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if src, ok := c.sources[repo]; ok {
		auth, err := c.authFor(repo)
		if err != nil {
			return nil, err
		}
		// A no-op for a local-path Source; the Source itself knows whether it
		// has a remote to fetch from.
		if err := src.Fetch(auth); err != nil {
			return nil, fmt.Errorf("reposource: fetch %q: %w", repo, err)
		}
		return src, nil
	}

	src, err := c.openOrClone(repo)
	if err != nil {
		return nil, err
	}
	c.sources[repo] = src
	return src, nil
}

// ResolveRepo adapts Get to subtree.Resolver, so the on-demand diff handlers
// resolve a repo through the same working copies the poller walks rather than
// opening their own. *gitsource.Source already satisfies subtree.Repo, so no
// wrapping is needed beyond the interface conversion.
func (c *Cache) ResolveRepo(repo string) (subtree.Repo, error) {
	src, err := c.Get(repo)
	if err != nil {
		return nil, err
	}
	return src, nil
}

// authFor returns the credentials to use for repo, or nil for unauthenticated
// access — which is the answer when no TokenProvider is configured, and when
// the repo is not an https:// URL.
//
// Refusing to attach credentials to a non-HTTPS remote is deliberate
// belt-and-suspenders: config validation already rejects http:// repos at
// load time, and this guarantees a token is never put on the wire in the clear
// even if some other code path introduces one later. The two rules are
// independent on purpose — this one is the last line, and it does not assume
// the first one ran.
func (c *Cache) authFor(repo string) (gogithttp.AuthMethod, error) {
	if c.tokens == nil {
		return nil, nil
	}
	if !strings.HasPrefix(repo, "https://") {
		return nil, nil
	}
	tok, err := c.tokens.Token()
	if err != nil {
		return nil, fmt.Errorf("reposource: get token for %q: %w", repo, err)
	}
	return &gogithttp.BasicAuth{Username: "x-access-token", Password: tok}, nil
}

// openOrClone opens a local path directly, or clones a remote URL into a
// stable directory derived from that URL.
func (c *Cache) openOrClone(repo string) (*gitsource.Source, error) {
	if !isRemoteURL(repo) {
		return gitsource.Open(repo)
	}

	auth, err := c.authFor(repo)
	if err != nil {
		return nil, err
	}
	return gitsource.OpenOrClone(repo, c.clonePath(repo), auth)
}

// clonePath is the local directory a remote repo is cloned into: stable across
// restarts, so a surviving clone directory is reopened rather than re-cloned.
func (c *Cache) clonePath(repo string) string {
	root := c.cloneRoot
	if root == "" {
		root = os.TempDir()
	}
	return filepath.Join(root, "ctd-clones", sanitizeRepoURL(repo))
}

// isRemoteURL reports whether repo names a remote rather than a local path.
// http:// is included so such a URL is treated as a remote and refused
// credentials by authFor, rather than being misread as a local path.
func isRemoteURL(repo string) bool {
	return strings.HasPrefix(repo, "http://") || strings.HasPrefix(repo, "https://")
}

// sanitizeRepoURL converts a URL into a filesystem-safe directory name by
// replacing scheme and path punctuation with dashes.
func sanitizeRepoURL(url string) string {
	r := strings.NewReplacer(
		"https://", "",
		"http://", "",
		"/", "-",
		":", "-",
		".", "-",
	)
	return r.Replace(url)
}
