package chartdiff

import (
	"fmt"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/manifestdiff"
)

// Conservative defaults applied by Config.Resolved when a field is left
// zero. Per ADR 0002, these bound a single-replica dashboard's exposure to a
// pathological or hostile render: a per-render timeout, a concurrency cap, an
// output size ceiling, an LRU cache size, and the materialization ceilings
// (bytes / files / depth) enforced in gitsource.
const (
	// DefaultRenderTimeout bounds a single Render call. Real charts render
	// in well under a second; 10s leaves headroom for a large umbrella
	// chart without letting one pathological render hold a slot long.
	DefaultRenderTimeout = 10 * time.Second
	// DefaultConcurrencyCap bounds how many renders may run at once. The
	// dashboard is a single-replica pod; a handful of concurrent Helm
	// renders is real CPU/memory work.
	DefaultConcurrencyCap = 4
	// DefaultMaxUnifiedBytes mirrors manifestdiff's own default output
	// ceiling, so the two stay in sync by default.
	DefaultMaxUnifiedBytes = manifestdiff.DefaultMaxUnifiedBytes
	// DefaultCacheEntries bounds the in-memory LRU's entry count. Each entry
	// retains a full unified diff, so this is a memory bound as much as a
	// hit-rate one.
	DefaultCacheEntries = 128
	// DefaultMaxMaterializedBytes bounds the total bytes MaterializeSubtreeBounded
	// may write for a single tenant subtree extraction. Real vendored
	// charts/*.tgz artifacts are typically kilobytes to a few megabytes;
	// 64 MiB leaves generous headroom while still bounding a hostile repo.
	DefaultMaxMaterializedBytes = 64 << 20 // 64 MiB
	// DefaultMaxMaterializedFiles bounds the file count a single
	// materialization may write. Real chart trees (umbrella + several
	// vendored subcharts) rarely exceed a few hundred files.
	DefaultMaxMaterializedFiles = 2000
	// DefaultMaxMaterializedDepth bounds tree recursion depth, guarding
	// against stack exhaustion from a maliciously deep git tree. Real chart
	// dependency nesting rarely exceeds single digits.
	DefaultMaxMaterializedDepth = 20
	// DefaultMaxMaterializedNodes bounds the total number of tree entries
	// (files and directories combined) a single materialization may visit —
	// independent of DefaultMaxMaterializedFiles, which only ever counts
	// actual files. A crafted tree of many empty directories has zero files
	// and zero bytes, so neither the file nor byte ceiling ever trips
	// against it; this is the ceiling that closes that gap. Real chart
	// trees (an umbrella plus several vendored subcharts) rarely exceed a
	// few hundred combined files and directories, so 5000 leaves generous
	// headroom for a legitimate, deeply-nested chart while still rejecting
	// a hostile tree of thousands of empty directories promptly.
	DefaultMaxMaterializedNodes = 5000
	// DefaultMaterializeTimeout bounds a single
	// MaterializeSubtreeBounded call, mirroring DefaultRenderTimeout's
	// rationale for the sibling render step: materializing a tenant subtree
	// from an already-cloned local git object store is normally
	// sub-second, so 10s leaves headroom without letting one pathological
	// or adversarial repository tree hold a materialize slot indefinitely.
	DefaultMaterializeTimeout = 10 * time.Second
	// DefaultMaterializeConcurrencyCap bounds how many materializations may
	// run concurrently, mirroring DefaultConcurrencyCap's rationale for the
	// sibling render step on a single-replica pod.
	DefaultMaterializeConcurrencyCap = 4
)

// Config bounds the chartdiff Engine's resource usage (ADR 0002): a
// per-render timeout, a render concurrency cap, the unified-diff output size
// ceiling, the LRU cache's entry count, and the materialization ceilings
// (total bytes / file count / recursion depth) passed through to
// gitsource.MaterializeSubtreeBounded.
//
// Every field's zero value means "use the conservative default" — see
// Resolved. This mirrors manifestdiff.Params.MaxUnifiedBytes's own
// "zero-or-negative means default" convention. An explicitly negative value
// is never silently accepted or defaulted; Resolved rejects it.
//
// Config is deliberately NOT wired into the global YAML config-file parsing
// / hot-reload here — that lands in a later web/config slice per the PRD's
// slice ordering. This package is self-contained: a caller constructs a
// Config literal (or the zero value) and passes it to NewEngine.
type Config struct {
	// RenderTimeout bounds a single chartrender.Render call.
	RenderTimeout time.Duration
	// ConcurrencyCap bounds how many renders may run concurrently.
	ConcurrencyCap int
	// MaxUnifiedBytes bounds the emitted unified diff text (passed through
	// to manifestdiff.Params.MaxUnifiedBytes).
	MaxUnifiedBytes int
	// CacheEntries bounds the in-memory LRU cache's entry count.
	CacheEntries int
	// MaxMaterializedBytes bounds the total bytes a single tenant subtree
	// materialization may write to disk.
	MaxMaterializedBytes int64
	// MaxMaterializedFiles bounds the file count a single tenant subtree
	// materialization may write.
	MaxMaterializedFiles int
	// MaxMaterializedDepth bounds tree recursion depth during
	// materialization.
	MaxMaterializedDepth int
	// MaxMaterializedNodes bounds the total number of tree entries (files
	// and directories combined) a single tenant subtree materialization may
	// visit, passed through to gitsource.MaterializeBounds.MaxTreeNodes.
	MaxMaterializedNodes int
	// MaterializeTimeout bounds a single
	// ChartRepo.MaterializeSubtreeBounded call — the sibling bound to
	// RenderTimeout for the other half of a Chart diff's per-side work.
	// Materialize and render have different resource profiles (a
	// disk/CPU tree walk vs. a Helm template execution), but both are
	// synchronous, uncancellable calls into untrusted-input-driven code, so
	// both need their own timeout for the same reason (see
	// materialize.go's materializeBounded).
	MaterializeTimeout time.Duration
	// MaterializeConcurrencyCap bounds how many materializations may run
	// concurrently, independently of ConcurrencyCap (which bounds
	// concurrent renders). A dedicated cap — rather than sharing
	// ConcurrencyCap/e.sem with render — is used deliberately: materialize
	// is a disk/CPU tree-walk workload and render is a CPU-bound Helm
	// template execution, two different resource profiles with their own
	// natural concurrency ceilings. Sharing one semaphore would let a burst
	// of slow materializations starve render slots (or vice versa) even
	// though the two steps compete for different underlying resources and
	// already have independent resource ceilings (bytes/files/depth/nodes
	// vs. render timeout) — a dedicated cap lets each be tuned
	// independently, consistent with this Config's existing one-field-per-
	// concern shape.
	MaterializeConcurrencyCap int
}

// Resolved returns a copy of c with every zero field replaced by its
// conservative default, after validating that no field is explicitly
// negative. A Config with an explicitly negative field is always rejected,
// even though the corresponding zero value would have been resolved to a
// default — zero means "unset," not "no bound."
func (c Config) Resolved() (Config, error) {
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c.withDefaults(), nil
}

// validate rejects a Config with any explicitly negative field. Zero is
// exempt — it is the "use the default" sentinel, not a bound of "0" (which
// would be useless in every one of these fields: no render time, no
// concurrency, no output, no cache, no materialized content at all).
func (c Config) validate() error {
	switch {
	case c.RenderTimeout < 0:
		return fmt.Errorf("chartdiff: RenderTimeout must not be negative, got %v", c.RenderTimeout)
	case c.ConcurrencyCap < 0:
		return fmt.Errorf("chartdiff: ConcurrencyCap must not be negative, got %d", c.ConcurrencyCap)
	case c.MaxUnifiedBytes < 0:
		return fmt.Errorf("chartdiff: MaxUnifiedBytes must not be negative, got %d", c.MaxUnifiedBytes)
	case c.CacheEntries < 0:
		return fmt.Errorf("chartdiff: CacheEntries must not be negative, got %d", c.CacheEntries)
	case c.MaxMaterializedBytes < 0:
		return fmt.Errorf("chartdiff: MaxMaterializedBytes must not be negative, got %d", c.MaxMaterializedBytes)
	case c.MaxMaterializedFiles < 0:
		return fmt.Errorf("chartdiff: MaxMaterializedFiles must not be negative, got %d", c.MaxMaterializedFiles)
	case c.MaxMaterializedDepth < 0:
		return fmt.Errorf("chartdiff: MaxMaterializedDepth must not be negative, got %d", c.MaxMaterializedDepth)
	case c.MaxMaterializedNodes < 0:
		return fmt.Errorf("chartdiff: MaxMaterializedNodes must not be negative, got %d", c.MaxMaterializedNodes)
	case c.MaterializeTimeout < 0:
		return fmt.Errorf("chartdiff: MaterializeTimeout must not be negative, got %v", c.MaterializeTimeout)
	case c.MaterializeConcurrencyCap < 0:
		return fmt.Errorf("chartdiff: MaterializeConcurrencyCap must not be negative, got %d", c.MaterializeConcurrencyCap)
	}
	return nil
}

// withDefaults returns a copy of c with every zero field replaced by its
// conservative default.
func (c Config) withDefaults() Config {
	if c.RenderTimeout == 0 {
		c.RenderTimeout = DefaultRenderTimeout
	}
	if c.ConcurrencyCap == 0 {
		c.ConcurrencyCap = DefaultConcurrencyCap
	}
	if c.MaxUnifiedBytes == 0 {
		c.MaxUnifiedBytes = DefaultMaxUnifiedBytes
	}
	if c.CacheEntries == 0 {
		c.CacheEntries = DefaultCacheEntries
	}
	if c.MaxMaterializedBytes == 0 {
		c.MaxMaterializedBytes = DefaultMaxMaterializedBytes
	}
	if c.MaxMaterializedFiles == 0 {
		c.MaxMaterializedFiles = DefaultMaxMaterializedFiles
	}
	if c.MaxMaterializedDepth == 0 {
		c.MaxMaterializedDepth = DefaultMaxMaterializedDepth
	}
	if c.MaxMaterializedNodes == 0 {
		c.MaxMaterializedNodes = DefaultMaxMaterializedNodes
	}
	if c.MaterializeTimeout == 0 {
		c.MaterializeTimeout = DefaultMaterializeTimeout
	}
	if c.MaterializeConcurrencyCap == 0 {
		c.MaterializeConcurrencyCap = DefaultMaterializeConcurrencyCap
	}
	return c
}
