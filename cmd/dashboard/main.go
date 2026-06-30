// Package main is the entry point for the change-tracking-dashboard binary.
// It wires Config → Git source(s) → Poller → Store → Web and serves the feed.
//
// Tracker configuration is loaded from a ConfigMap YAML file (path via the
// CONFIG_PATH environment variable). The file is watched and hot-reloaded on
// change: added/removed trackers take effect on the next poll cycle without
// a restart.
//
// Each distinct repo path gets its own gitsource.Source (a small in-process
// cache keyed by path). DB_PATH and LISTEN_ADDR are still env-driven; they
// are operational config, not tracker config.
//
// GitHub App token auth for remote repos is enabled when the three env vars
// GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID, and GITHUB_APP_PRIVATE_KEY_FILE
// are all set (injected from a Kubernetes Secret). When present, remote HTTPS
// repos are cloned/fetched using a short-lived installation token. Local paths
// continue to use PlainOpen unchanged.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/config"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/githubapp"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/poller"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/scheduler"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/store"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/web"
	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// defaultConfigPath is used when CONFIG_PATH is not set.
const defaultConfigPath = "/etc/dashboard/config.yaml"

// HTTP server timeouts guard against slow-client (slow-loris) attacks and
// connections held open indefinitely.
const (
	serverReadTimeout  = 10 * time.Second
	serverWriteTimeout = 30 * time.Second
	serverIdleTimeout  = 120 * time.Second
)

func main() {
	configPath := envOrDefault("CONFIG_PATH", defaultConfigPath)
	dbPath := envOrDefault("DB_PATH", "changes.db")
	listenAddr := envOrDefault("LISTEN_ADDR", ":8080")

	if err := run(configPath, dbPath, listenAddr); err != nil {
		log.Fatalf("dashboard: %v", err)
	}
}

func run(configPath, dbPath, listenAddr string) error {
	// --- Config ---
	cfgWatcher, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config from %q: %w", configPath, err)
	}
	log.Printf("dashboard: loaded config from %s (%d trackers)", configPath, len(cfgWatcher.Current().Trackers))

	// --- Store ---
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return fmt.Errorf("create db dir: %w", err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// --- GitHub App token provider (optional) ---
	// When all three env vars are set the provider mints short-lived installation
	// tokens; remote HTTPS repos are then cloned/fetched authenticated.
	// When the vars are absent the provider is nil and local-path repos work as before.
	var tokenProvider *githubapp.Provider
	appCfg, enabled, err := githubapp.FromEnv()
	if err != nil {
		return fmt.Errorf("github app config: %w", err)
	}
	if enabled {
		p, err := githubapp.New(appCfg)
		if err != nil {
			return fmt.Errorf("github app provider: %w", err)
		}
		tokenProvider = p
		log.Printf("dashboard: GitHub App auth enabled (appID=%d, installationID=%d)",
			appCfg.AppID, appCfg.InstallationID)
	}

	// --- Per-repo gitsource cache ---
	sources := newSourceCache(tokenProvider)

	// --- Per-tracker scheduler ---
	// The scheduler calls Tick on a 1s base interval, passing the latest
	// tracker list from the config watcher each time. Trackers added or removed
	// by a config reload take effect on the next Tick automatically.
	// Each tracker fires on its own PollIntervalSeconds cadence.
	pollFn := func(t domain.Tracker) error {
		src, err := sources.get(t.Repo)
		if err != nil {
			return fmt.Errorf("open git source for %q: %w", t.Repo, err)
		}
		p := poller.New(src, st)
		return p.Poll(t)
	}

	sched := scheduler.New(time.Now, scheduler.PollFunc(pollFn))

	go func() {
		ticker := time.NewTicker(scheduler.BaseTickInterval)
		defer ticker.Stop()
		for range ticker.C {
			current := cfgWatcher.Current()
			sched.Tick(current.Trackers)
		}
	}()

	// --- HTTP ---
	handler := web.NewHandler(st)
	mux := http.NewServeMux()
	mux.Handle("/", handler)

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      mux,
		ReadTimeout:  serverReadTimeout,
		WriteTimeout: serverWriteTimeout,
		IdleTimeout:  serverIdleTimeout,
	}

	log.Printf("dashboard: listening on %s", listenAddr)
	return srv.ListenAndServe()
}

// sourceCache is a small thread-safe map from repo path → *gitsource.Source.
// Opening a gitsource is cheap (local disk), but we avoid re-opening the same
// repo on every poll cycle.
//
// When a tokenProvider is set, remote HTTPS repo URLs are cloned into a
// local cache directory and authenticated with a fresh installation token.
// Local paths continue to use PlainOpen.
type sourceCache struct {
	mu            sync.Mutex
	sources       map[string]*gitsource.Source
	tokenProvider *githubapp.Provider // nil when GitHub App auth is disabled
}

func newSourceCache(tp *githubapp.Provider) *sourceCache {
	return &sourceCache{
		sources:       make(map[string]*gitsource.Source),
		tokenProvider: tp,
	}
}

// get returns the cached Source for the given repo path or URL, opening or
// cloning it if necessary.
func (c *sourceCache) get(repo string) (*gitsource.Source, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if src, ok := c.sources[repo]; ok {
		return src, nil
	}

	src, err := c.openOrClone(repo)
	if err != nil {
		return nil, err
	}
	c.sources[repo] = src
	return src, nil
}

// openOrClone opens a local path directly, or clones a remote HTTPS URL with
// optional GitHub App token auth into a local directory under the system temp dir.
func (c *sourceCache) openOrClone(repo string) (*gitsource.Source, error) {
	if isRemoteURL(repo) {
		var auth gogithttp.AuthMethod
		if c.tokenProvider != nil {
			tok, err := c.tokenProvider.Token()
			if err != nil {
				return nil, fmt.Errorf("get installation token for %q: %w", repo, err)
			}
			auth = &gogithttp.BasicAuth{
				Username: "x-access-token",
				Password: tok,
			}
		}
		// Use a stable local clone directory derived from the repo URL so the
		// source cache survives process restarts without re-cloning from scratch.
		localPath := filepath.Join(os.TempDir(), "ctd-clones", sanitizeRepoURL(repo))
		return gitsource.OpenOrClone(repo, localPath, auth)
	}

	// Local path: use the existing PlainOpen path.
	return gitsource.Open(repo)
}

// isRemoteURL returns true for http:// and https:// repo URLs.
func isRemoteURL(repo string) bool {
	return strings.HasPrefix(repo, "http://") || strings.HasPrefix(repo, "https://")
}

// sanitizeRepoURL converts a URL to a filesystem-safe directory name by
// replacing path separators and scheme characters with dashes.
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

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
