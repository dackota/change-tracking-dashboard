// Package main is the entry point for the change-tracking-dashboard binary.
// It wires Config → Git source(s) → Poller → Store → Web and serves the
// timeline page.
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
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartdiff"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/config"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/githubapp"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/poller"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/pollstatus"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/scheduler"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/store"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/telemetry"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/web"
	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"go.opentelemetry.io/otel"
)

// defaultConfigPath is used when CONFIG_PATH is not set.
const defaultConfigPath = "/etc/dashboard/config.yaml"

// serviceName is the OTel "service.name" resource attribute for every span,
// metric point, and structured log line this process emits.
const serviceName = "change-tracking-dashboard"

// HTTP server timeouts guard against slow-client (slow-loris) attacks and
// connections held open indefinitely.
const (
	serverReadTimeout  = 10 * time.Second
	serverWriteTimeout = 30 * time.Second
	serverIdleTimeout  = 120 * time.Second
)

// telemetryInitTimeout/telemetryShutdownTimeout bound the OTLP exporter's
// setup and final flush respectively — an unreachable/slow collector must
// never hang startup or shutdown indefinitely.
const (
	telemetryInitTimeout     = 5 * time.Second
	telemetryShutdownTimeout = 5 * time.Second
	serverShutdownTimeout    = 10 * time.Second
)

func main() {
	configPath := envOrDefault("CONFIG_PATH", defaultConfigPath)
	dbPath := envOrDefault("DB_PATH", "changes.db")
	listenAddr := envOrDefault("LISTEN_ADDR", ":8080")

	logger := telemetry.NewLogger(serviceName, os.Stderr)
	if err := run(configPath, dbPath, listenAddr, logger); err != nil {
		logger.Error("dashboard: fatal error", "error", err)
		os.Exit(1)
	}
}

func run(configPath, dbPath, listenAddr string, logger *slog.Logger) error {
	// --- Config ---
	cfgWatcher, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config from %q: %w", configPath, err)
	}
	logger.Info("dashboard: loaded config", "path", configPath, "trackers", len(cfgWatcher.Current().Trackers))

	// --- Telemetry ---
	// The OTLP endpoint is sourced from observability.otlp_endpoint in the
	// tracker ConfigMap, or the standard OTEL_EXPORTER_OTLP_ENDPOINT env var
	// (which always takes precedence when set — see ResolveOTLPEndpoint). An
	// empty value degrades safely: the SDK still initializes (spans/metrics
	// carry real IDs for log correlation) but exports nothing, since no
	// backend is assumed to exist.
	otlpEndpoint := telemetry.ResolveOTLPEndpoint(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), cfgWatcher.Current().Observability.OTLPEndpoint)
	initCtx, initCancel := context.WithTimeout(context.Background(), telemetryInitTimeout)
	sdk, err := telemetry.Init(initCtx, telemetry.Config{ServiceName: serviceName, OTLPEndpoint: otlpEndpoint})
	initCancel()
	if err != nil {
		return fmt.Errorf("init telemetry: %w", err)
	}
	otel.SetTracerProvider(sdk.TracerProvider)
	otel.SetMeterProvider(sdk.MeterProvider)
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), telemetryShutdownTimeout)
		defer shutdownCancel()
		if err := sdk.Shutdown(shutdownCtx); err != nil {
			logger.Error("dashboard: telemetry shutdown failed", "error", err)
		}
	}()

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
		logger.Info("dashboard: GitHub App auth enabled", "appID", appCfg.AppID, "installationID", appCfg.InstallationID)
	}

	// --- Per-repo gitsource cache ---
	sources := newSourceCache(tokenProvider)

	// --- Poll status registry ---
	// Records, per tracker, the last attempt/success time and last error —
	// in-process only, no persistence; it rebuilds naturally on restart. Its
	// Snapshot() is wired into every page handler's shared header (the
	// aggregate poll-status chip) and into the Trackers view's per-tracker
	// status columns; /healthz is a dependency-free liveness check and does
	// not consume it.
	pollStatus := pollstatus.New()

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
		p := poller.New(src, st,
			poller.WithTracerProvider(sdk.TracerProvider),
			poller.WithMeterProvider(sdk.MeterProvider),
			poller.WithLogger(logger),
		)
		return p.Poll(t)
	}

	sched := scheduler.New(time.Now, scheduler.PollFunc(pollFn), pollStatus)

	go func() {
		ticker := time.NewTicker(scheduler.BaseTickInterval)
		defer ticker.Stop()
		for range ticker.C {
			current := cfgWatcher.Current()
			sched.Tick(current.Trackers)
		}
	}()

	// --- Chart diff engine ---
	// chartdiff.Config{} (all-zero) resolves to the package's conservative
	// defaults (see chartdiff/config.go). Wiring the config file's
	// timeout/concurrency/cache-size/materialize fields through is deferred
	// to a later slice per the chart-diff PRD's slice ordering.
	chartDiffEngine, err := chartdiff.NewEngine(chartdiff.Config{}, nil, chartdiff.WithTracerProvider(sdk.TracerProvider))
	if err != nil {
		return fmt.Errorf("create chart diff engine: %w", err)
	}

	// --- HTTP ---
	timelineHandler := web.NewTimelineHandler(st, pollStatus)
	staticHandler := web.NewStaticHandler()
	changesetsHandler := web.NewChangesetsHandler(st)
	changesetDetailHandler := web.NewChangesetDetailHandler(st)
	chartDiffHandler := web.NewChartDiffHandler(chartDiffEngine, sources, st)
	trackersHandler := web.NewTrackersHandler(cfgWatcher, pollStatus)
	repositoriesHandler := web.NewRepositoriesHandler(st, pollStatus)
	changesHandler := web.NewChangesHandler(pollStatus)
	healthzHandler := web.NewHealthzHandler()
	mux := http.NewServeMux()
	mux.Handle("/", timelineHandler)
	mux.Handle("/static/", staticHandler)
	mux.Handle("/api/changesets", changesetsHandler)
	mux.Handle("/api/changesets/detail", changesetDetailHandler)
	mux.Handle("/api/changesets/detail/chart-diff", chartDiffHandler)
	mux.Handle("GET /trackers", trackersHandler)
	mux.Handle("GET /repositories", repositoriesHandler)
	mux.Handle("GET /changes", changesHandler)
	mux.Handle("GET /healthz", healthzHandler)

	// RED middleware wraps the whole mux, so every route above — present or
	// future — emits the generic RED signal and correlated structured logs
	// without each handler needing its own instrumentation.
	httpRed, err := telemetry.NewREDMetrics(sdk.MeterProvider, "http")
	if err != nil {
		return fmt.Errorf("create HTTP RED metrics: %w", err)
	}
	instrumentedMux := telemetry.Middleware(mux, sdk.TracerProvider.Tracer("http"), httpRed, logger)

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      instrumentedMux,
		ReadTimeout:  serverReadTimeout,
		WriteTimeout: serverWriteTimeout,
		IdleTimeout:  serverIdleTimeout,
	}

	// Graceful shutdown: on SIGINT/SIGTERM, stop accepting new connections
	// and let in-flight requests finish (bounded by serverShutdownTimeout)
	// before the deferred telemetry Shutdown above flushes pending
	// spans/metrics — an abrupt process kill would otherwise drop them.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErrCh := make(chan error, 1)
	go func() {
		logger.Info("dashboard: listening", "addr", listenAddr)
		serveErrCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErrCh:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("dashboard: shutting down")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		return nil
	}
}

// cachedSource bundles a gitsource.Source with the remote URL it was cloned
// from (empty string for local-path sources). The remoteURL is used on every
// subsequent poll cycle to perform an authenticated fetch so that commits pushed
// to the remote after the initial clone are observed by WalkCommits.
type cachedSource struct {
	src       *gitsource.Source
	remoteURL string // empty for local-path sources — no fetch needed
}

// sourceCache is a small thread-safe map from repo path/URL → cachedSource.
// Opening a gitsource is cheap (local disk), but we avoid re-opening the same
// repo on every poll cycle.
//
// For remote HTTPS repos the cache also performs an authenticated fetch on every
// get() call so that newly-pushed commits are visible within each poll cycle.
// Local paths continue to use PlainOpen without any fetch.
//
// NOTE: clone directories are placed under os.TempDir() (typically tmpfs on
// many Kubernetes nodes). They are therefore ephemeral: a pod restart re-clones
// from scratch. This is intentional — the store's high-water-mark resumes
// incremental polling correctly after a re-clone with no data loss.
type sourceCache struct {
	mu            sync.Mutex
	sources       map[string]*cachedSource
	tokenProvider *githubapp.Provider // nil when GitHub App auth is disabled
}

func newSourceCache(tp *githubapp.Provider) *sourceCache {
	return &sourceCache{
		sources:       make(map[string]*cachedSource),
		tokenProvider: tp,
	}
}

// get returns the Source for the given repo path or URL. On the first call it
// opens or clones the repo; on every subsequent call for a remote-backed source
// it performs an authenticated fetch so that newly-pushed commits are visible to
// WalkCommits within the same poll cycle.
func (c *sourceCache) get(repo string) (*gitsource.Source, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if cs, ok := c.sources[repo]; ok {
		// Already cloned — fetch from the remote (if any) to pick up new commits.
		if cs.remoteURL != "" {
			auth, err := c.buildAuth(repo)
			if err != nil {
				return nil, err
			}
			if err := cs.src.Fetch(cs.remoteURL, auth); err != nil {
				return nil, fmt.Errorf("fetch %q: %w", repo, err)
			}
		}
		return cs.src, nil
	}

	cs, err := c.openOrClone(repo)
	if err != nil {
		return nil, err
	}
	c.sources[repo] = cs
	return cs.src, nil
}

// ResolveChartRepo adapts sourceCache.get to web.ChartRepoResolver, letting
// the chart-diff handler resolve/clone a repo via the same source cache
// every poller and the timeline detail handler use. *gitsource.Source
// already satisfies chartdiff.ChartRepo directly, so no further wrapping is
// needed beyond the interface conversion.
func (c *sourceCache) ResolveChartRepo(repo string) (chartdiff.ChartRepo, error) {
	src, err := c.get(repo)
	if err != nil {
		return nil, err
	}
	return src, nil
}

// buildAuth constructs the BasicAuth for the given remote repo URL. Returns nil
// when no tokenProvider is configured (unauthenticated access) or when the
// remote is not an https:// URL — credentials are never attached to non-HTTPS
// remotes so an on-path observer cannot capture the installation token.
//
// This is a belt-and-suspenders guard: the config validator already rejects
// http:// repos at load time; this ensures auth is never attached even if a
// non-https URL reaches this path through an unexpected code route.
func (c *sourceCache) buildAuth(repo string) (gogithttp.AuthMethod, error) {
	if c.tokenProvider == nil {
		return nil, nil
	}
	// Never send credentials over a non-HTTPS transport.
	if !strings.HasPrefix(repo, "https://") {
		return nil, nil
	}
	tok, err := c.tokenProvider.Token()
	if err != nil {
		return nil, fmt.Errorf("get installation token for %q: %w", repo, err)
	}
	return &gogithttp.BasicAuth{
		Username: "x-access-token",
		Password: tok,
	}, nil
}

// openOrClone opens a local path directly, or clones a remote HTTPS URL with
// optional GitHub App token auth into a local directory under the system temp dir.
func (c *sourceCache) openOrClone(repo string) (*cachedSource, error) {
	if isRemoteURL(repo) {
		auth, err := c.buildAuth(repo)
		if err != nil {
			return nil, err
		}
		// Clone into a stable local directory derived from the repo URL.
		// Clones are ephemeral (os.TempDir() / tmpfs on many k8s nodes);
		// the store's high-water-mark resumes polling after a pod restart
		// with no data loss — see sourceCache doc comment.
		localPath := filepath.Join(os.TempDir(), "ctd-clones", sanitizeRepoURL(repo))
		src, err := gitsource.OpenOrClone(repo, localPath, auth)
		if err != nil {
			return nil, err
		}
		return &cachedSource{src: src, remoteURL: repo}, nil
	}

	// Local path: use the existing PlainOpen path; no remote to fetch.
	src, err := gitsource.Open(repo)
	if err != nil {
		return nil, err
	}
	return &cachedSource{src: src, remoteURL: ""}, nil
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
