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
// GitHub App token auth for remote repos is out of scope here (github-app-auth).
// Per-tracker poll cadence and backfill window are read from the config and
// honored by the scheduler, which calls Tick on a 1s base interval.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/config"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/poller"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/scheduler"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/store"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/web"
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

	// --- Per-repo gitsource cache ---
	sources := newSourceCache()

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
type sourceCache struct {
	mu      sync.Mutex
	sources map[string]*gitsource.Source
}

func newSourceCache() *sourceCache {
	return &sourceCache{sources: make(map[string]*gitsource.Source)}
}

// get returns the cached Source for path, opening it if necessary.
func (c *sourceCache) get(path string) (*gitsource.Source, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if src, ok := c.sources[path]; ok {
		return src, nil
	}

	src, err := gitsource.Open(path)
	if err != nil {
		return nil, err
	}
	c.sources[path] = src
	return src, nil
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
