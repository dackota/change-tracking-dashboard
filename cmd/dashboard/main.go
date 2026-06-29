// Package main is the entry point for the change-tracking-dashboard binary.
// It wires Config → Git source → Poller → Store → Web and serves the feed.
//
// Config is hardcoded as a minimal struct literal for this skeleton slice.
// Full ConfigMap parsing, validation, and hot-reload are a SEPARATE downstream
// task (hot-reload-config-validation) — do not build them here.
//
// GitHub App token auth is also out of scope here (github-app-auth task).
// The git source accepts a local repo path — sufficient for the skeleton.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/poller"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/store"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/web"
)

// skeletonConfig is the hardcoded skeleton configuration.
// Replace with full config loading in the hot-reload-config-validation task.
type skeletonConfig struct {
	RepoPath  string
	DBPath    string
	ListenAddr string
	Trackers  []domain.Tracker
}

func defaultConfig() skeletonConfig {
	repoPath := envOrDefault("REPO_PATH", ".")
	dbPath := envOrDefault("DB_PATH", "changes.db")
	addr := envOrDefault("LISTEN_ADDR", ":8080")

	return skeletonConfig{
		RepoPath:   repoPath,
		DBPath:     dbPath,
		ListenAddr: addr,
		Trackers: []domain.Tracker{
			{
				Repo:          repoPath,
				FileGlob:      "Chart.yaml",
				Field:         "chart-version",
				ExtractorExpr: ".version",
				FacetPattern:  `^apps/(?P<tenant>[^/]+)/(?P<env>[^/]+)/(?P<region>[^/]+)/`,
			},
		},
	}
}

func main() {
	cfg := defaultConfig()

	if err := run(cfg); err != nil {
		log.Fatalf("dashboard: %v", err)
	}
}

func run(cfg skeletonConfig) error {
	// Ensure DB directory exists.
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return fmt.Errorf("create db dir: %w", err)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	src, err := gitsource.Open(cfg.RepoPath)
	if err != nil {
		return fmt.Errorf("open git source: %w", err)
	}

	p := poller.New(src, st)

	// Perform an initial poll synchronously so the first page load has data.
	for _, t := range cfg.Trackers {
		if err := p.Poll(t); err != nil {
			log.Printf("poll (initial, tracker %q): %v", t.Field, err)
		}
	}

	// Background poll loop.
	// TODO (backfill-and-poll-config task): make interval configurable.
	const pollInterval = 30 * time.Second
	go func() {
		for range time.Tick(pollInterval) {
			for _, t := range cfg.Trackers {
				if err := p.Poll(t); err != nil {
					log.Printf("poll (background, tracker %q): %v", t.Field, err)
				}
			}
		}
	}()

	handler := web.NewHandler(st)
	mux := http.NewServeMux()
	mux.Handle("/", handler)

	log.Printf("dashboard: listening on %s", cfg.ListenAddr)
	return http.ListenAndServe(cfg.ListenAddr, mux)
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
