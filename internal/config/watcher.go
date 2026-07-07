// watcher.go implements the hot-reload Watcher. It holds the path to the
// config file and the last-good Config snapshot, guarded by a mutex.
//
// Hot-reload strategy: polling (re-reads + hashes the file every watchInterval).
// Kubernetes ConfigMap volume mounts update via atomic symlink swap, which
// makes inotify/fsnotify unreliable. Simple polling avoids that caveat with
// no additional dependency.
//
// On a reload whose new content fails validation, the watcher logs a clear
// error and keeps serving the last-good config — it does NOT crash.
package config

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
)

// watchInterval is the period between background file-hash checks.
// Kubernetes ConfigMap volume updates typically propagate within ~10s, so
// this interval provides timely pick-up without excess I/O.
const watchInterval = 10 * time.Second

// Watcher holds the current validated Config and watches the config file for
// changes. Call Current() to get a thread-safe snapshot of the live config.
type Watcher struct {
	path     string
	mu       sync.RWMutex
	current  *Config
	lastHash [sha256.Size]byte
}

// Load parses and validates the config at path, then returns a *Watcher ready
// to serve the config. The background poll loop starts automatically.
// Returns a fatal error if the file is missing or the initial config is invalid.
func Load(path string) (*Watcher, error) {
	cfg, err := parseFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: initial load: %w", err)
	}

	h, err := fileHash(path)
	if err != nil {
		return nil, fmt.Errorf("config: hash initial file: %w", err)
	}

	w := &Watcher{
		path:     path,
		current:  cfg,
		lastHash: h,
	}

	go w.pollLoop()
	return w, nil
}

// Current returns a snapshot of the current valid config. The returned value is
// a deep copy, so callers may freely read (or even mutate) it without affecting
// the live config shared with the background reload goroutine.
func (w *Watcher) Current() *Config {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.current.clone()
}

// Reload re-reads and re-validates the config file immediately. On success
// the snapshot is atomically swapped. On failure the last-good config is
// retained and the error is returned (and logged).
//
// This is exported primarily for deterministic testing — callers can trigger
// a reload without waiting for the background poll interval.
func (w *Watcher) Reload() error {
	h, err := fileHash(w.path)
	if err != nil {
		telemetry.LoggerFromContext(context.Background()).Error("config: reload hash error", "error", err)
		return fmt.Errorf("config: reload hash: %w", err)
	}

	cfg, err := parseFile(w.path)
	if err != nil {
		telemetry.LoggerFromContext(context.Background()).Error("config: reload validation error, keeping last-good config", "error", err)
		return err
	}

	w.mu.Lock()
	w.current = cfg
	w.lastHash = h
	w.mu.Unlock()

	return nil
}

// pollLoop is the background goroutine that re-hashes the file on watchInterval
// and calls Reload when the hash changes.
func (w *Watcher) pollLoop() {
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()

	for range ticker.C {
		h, err := fileHash(w.path)
		if err != nil {
			telemetry.LoggerFromContext(context.Background()).Error("config: watch hash error", "error", err)
			continue
		}

		w.mu.RLock()
		same := w.lastHash == h
		w.mu.RUnlock()

		if same {
			continue
		}

		if err := w.Reload(); err != nil {
			// Error already logged by Reload; keep looping.
			continue
		}
		telemetry.LoggerFromContext(context.Background()).Info("config: reloaded", "path", w.path)
	}
}

// fileHash returns the SHA-256 of the (size-capped) file at path. Using the
// same capped reader as parsing means an over-limit file is rejected here too,
// rather than hashed in full on every poll tick.
func fileHash(path string) ([sha256.Size]byte, error) {
	data, err := readConfigFile(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(data), nil
}
