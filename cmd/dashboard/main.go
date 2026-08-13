// Package main is the entry point for the change-tracking-dashboard binary.
// It wires Config → Git source(s) → Poller → Store → Web and serves the
// timeline page.
//
// Tracker configuration is loaded from a ConfigMap YAML file (path via the
// CONFIG_PATH environment variable). The file is watched and hot-reloaded on
// change: added/removed trackers take effect on the next poll cycle without
// a restart.
//
// This package owns process concerns only: environment, config loading,
// telemetry setup, the poll scheduler's ticker, and server lifecycle. The HTTP
// surface is internal/web's NewMux, and tracked repos' working copies are
// internal/reposource's Cache. DB_PATH and LISTEN_ADDR are still env-driven;
// they are operational config, not tracker config.
//
// GitHub App token auth for remote repos is enabled when the three env vars
// GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID, and GITHUB_APP_PRIVATE_KEY_FILE
// are all set (injected from a Kubernetes Secret). When present, remote HTTPS
// repos are cloned/fetched using a short-lived installation token. Local paths
// continue to use PlainOpen unchanged.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/chartdiff"
	"github.com/dackota/change-tracking-dashboard/internal/config"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/githubapp"
	"github.com/dackota/change-tracking-dashboard/internal/plandiff"
	"github.com/dackota/change-tracking-dashboard/internal/poller"
	"github.com/dackota/change-tracking-dashboard/internal/pollstatus"
	"github.com/dackota/change-tracking-dashboard/internal/reposource"
	"github.com/dackota/change-tracking-dashboard/internal/scheduler"
	"github.com/dackota/change-tracking-dashboard/internal/store"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
	"github.com/dackota/change-tracking-dashboard/internal/web"
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
	//
	// Export headers come from OTEL_EXPORTER_OTLP_HEADERS, or from
	// HONEYCOMB_API_KEY (injected from a Secret) which is shorthand for the
	// x-honeycomb-team header. Sending to Honeycomb is therefore just two env
	// vars — endpoint api.honeycomb.io:443 plus the key — with no code change.
	otlpEndpoint := telemetry.ResolveOTLPEndpoint(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), cfgWatcher.Current().Observability.OTLPEndpoint)
	otlpHeaders := telemetry.ResolveOTLPHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"), os.Getenv("HONEYCOMB_API_KEY"))
	initCtx, initCancel := context.WithTimeout(context.Background(), telemetryInitTimeout)
	sdk, err := telemetry.Init(initCtx, telemetry.Config{
		ServiceName:  serviceName,
		OTLPEndpoint: otlpEndpoint,
		Headers:      otlpHeaders,
	})
	initCancel()
	if err != nil {
		return fmt.Errorf("init telemetry: %w", err)
	}
	// Header *names* only — the values are credentials. This line is the
	// difference between "exports are silently rejected" and "auth is
	// configured" being diagnosable from the logs.
	logger.Info("dashboard: telemetry initialized",
		"otlp_endpoint", otlpEndpoint,
		"otlp_header_names", headerNames(otlpHeaders),
	)
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
	defer func() {
		if err := st.Close(); err != nil {
			logger.Error("dashboard: store close failed", "error", err)
		}
	}()

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

	// --- Per-repo working copies ---
	var sourceOpts []reposource.Option
	if tokenProvider != nil {
		sourceOpts = append(sourceOpts, reposource.WithTokenProvider(tokenProvider))
	}
	sources := reposource.New(sourceOpts...)

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
	//
	// The scheduler hands over the due trackers batched by (Repo, FileGlob) so
	// the poller walks each matched file's history once for the whole group
	// instead of once per field — every field in a group derives from the same
	// histories, and that walk is where the poller's CPU goes (#137).
	pollFn := func(group []domain.Tracker) []error {
		src, err := sources.Get(group[0].Repo)
		if err != nil {
			err = fmt.Errorf("open git source for %q: %w", group[0].Repo, err)
			errs := make([]error, len(group))
			for i := range errs {
				errs[i] = err
			}
			return errs
		}
		p := poller.New(src, st,
			poller.WithTracerProvider(sdk.TracerProvider),
			poller.WithMeterProvider(sdk.MeterProvider),
			poller.WithLogger(logger),
			poller.WithExtractFailureRecorder(pollStatus),
		)
		return p.PollGroup(group)
	}

	sched := scheduler.New(time.Now, pollFn, pollStatus)

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

	// --- Terraform static plan-diff engine ---
	// plandiff.Config{} (all-zero) resolves to the package's conservative
	// defaults (see plandiff/config.go), mirroring chartDiffEngine's own
	// zero-Config convention above. WithOutcomeRecorder wires the same
	// pollStatus registry chartDiffEngine has no equivalent for yet, so
	// every Diff outcome's Kind is counted on the poll-health/status surface
	// (acceptance criterion 9).
	planDiffEngine, err := plandiff.NewEngine(plandiff.Config{}, nil,
		plandiff.WithTracerProvider(sdk.TracerProvider),
		plandiff.WithOutcomeRecorder(pollStatus),
	)
	if err != nil {
		return fmt.Errorf("create plan diff engine: %w", err)
	}

	// --- HTTP ---
	// The routing table lives in internal/web so it can be booted and
	// exercised without this binary; main supplies the collaborators and
	// wraps the result in edge concerns.
	mux, err := web.NewMux(web.Deps{
		Store:      st,
		PollHealth: pollStatus,
		Config:     cfgWatcher,
		ChartDiff:  chartDiffEngine,
		PlanDiff:   planDiffEngine,
		Repos:      sources,
	})
	if err != nil {
		return fmt.Errorf("build http mux: %w", err)
	}

	// RED middleware wraps the whole mux, so every route — present or
	// future — emits the generic RED signal and correlated structured logs
	// without each handler needing its own instrumentation.
	httpRed, err := telemetry.NewREDMetrics(sdk.MeterProvider, "http")
	if err != nil {
		return fmt.Errorf("create HTTP RED metrics: %w", err)
	}
	// /healthz is the Kubernetes probe target: several hits a minute, forever,
	// carrying no information when they succeed. Its request log line is
	// suppressed so real entries are not buried; its RED metrics and spans are
	// not, and a 5xx on it is still logged. See telemetry.WithQuietRoutes.
	// VISITOR_ID_SALT enables the visitor.id span attribute (unique-visitor
	// counts); unset leaves it off. It must be the same value across replicas
	// — see telemetry.VisitorIdentity — so it belongs in a Secret, not in a
	// per-pod generated value.
	visitorIdentity := telemetry.VisitorIdentity{
		Salt:              os.Getenv("VISITOR_ID_SALT"),
		TrustForwardedFor: os.Getenv("TRUST_FORWARDED_FOR") == "true",
	}
	// Geo attributes are lifted from headers an upstream proxy sets (a Traefik
	// MaxMind plugin, a CDN); unset header names leave the dimension off. The
	// names are configurable because they differ per plugin.
	geoHeaders := telemetry.GeoHeaders{
		CountryISOCode: os.Getenv("GEO_COUNTRY_HEADER"),
		RegionISOCode:  os.Getenv("GEO_REGION_HEADER"),
		CityName:       os.Getenv("GEO_CITY_HEADER"),
	}
	instrumentedMux := telemetry.Middleware(mux, sdk.TracerProvider.Tracer("http"), httpRed, logger,
		telemetry.WithQuietRoutes(web.HealthzRoute),
		telemetry.WithVisitorIdentity(visitorIdentity),
		telemetry.WithGeoHeaders(geoHeaders))

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
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
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

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// headerNames returns the sorted header names in headers, for logging which
// OTLP headers are configured without ever logging their values (they are
// API keys). Sorted so the startup line is stable across restarts.
func headerNames(headers map[string]string) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
