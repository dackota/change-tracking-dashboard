package plandiff

import (
	"fmt"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/manifestdiff"
)

// Conservative defaults applied by Config.Resolved when a field is left
// zero, mirroring chartdiff.Config's own defaults field-for-field (R20: "the
// same resource bounds as chartdiff") — a per-diff timeout, a concurrency
// cap, an output size ceiling, an LRU cache size, the materialization
// ceilings (bytes/files/depth/nodes), and two fields with no chartdiff
// analogue: a nested-HCL-block recursion depth cap, and the default
// replacement-forcing attribute name list (see Config.ForceReplacementAttrs).
const (
	// DefaultParseTimeout bounds a single Parser.Parse call — the plandiff
	// analogue of chartdiff's DefaultRenderTimeout. Parsing a materialized
	// Terraform directory (already bounded by the materialize ceilings
	// below) is normally well under a second; 10s leaves headroom without
	// letting one pathological or adversarial input hold a slot long.
	DefaultParseTimeout = 10 * time.Second
	// DefaultConcurrencyCap bounds how many parses may run at once.
	DefaultConcurrencyCap = 4
	// DefaultMaxUnifiedBytes mirrors manifestdiff's own default output
	// ceiling, so the two stay in sync by default.
	DefaultMaxUnifiedBytes = manifestdiff.DefaultMaxUnifiedBytes
	// DefaultCacheEntries bounds the in-memory LRU's entry count.
	DefaultCacheEntries = 128
	// DefaultMaxMaterializedBytes bounds the total bytes
	// MaterializeSubtreeBounded may write for a single stack/module subtree
	// extraction. Real Terraform root modules are typically kilobytes to a
	// few hundred kilobytes of HCL; 64 MiB leaves generous headroom while
	// still bounding a hostile repo.
	DefaultMaxMaterializedBytes = 64 << 20 // 64 MiB
	// DefaultMaxMaterializedFiles bounds the file count a single
	// materialization may write.
	DefaultMaxMaterializedFiles = 2000
	// DefaultMaxMaterializedDepth bounds tree recursion depth during
	// materialization, guarding against stack exhaustion from a
	// maliciously deep git tree.
	DefaultMaxMaterializedDepth = 20
	// DefaultMaxMaterializedNodes bounds the total number of tree entries
	// (files and directories combined) a single materialization may visit.
	DefaultMaxMaterializedNodes = 5000
	// DefaultMaterializeTimeout bounds a single MaterializeSubtreeBounded
	// call.
	DefaultMaterializeTimeout = 10 * time.Second
	// DefaultMaterializeConcurrencyCap bounds how many materializations may
	// run concurrently, independently of ConcurrencyCap.
	DefaultMaterializeConcurrencyCap = 4
	// DefaultMaxBlockDepth bounds the nested-HCL-block recursion depth a
	// single resource body may be rendered to (see resource.go's
	// renderBody). This has no chartdiff analogue: chartrender's Helm SDK
	// owns its own template-execution recursion limits internally, but
	// plandiff's own HCL-body renderer is hand-written and walks
	// hclsyntax.Body.Blocks recursively, so it needs its own explicit
	// ceiling to stay a total function against an adversarially
	// deeply-nested (but otherwise small, within-byte-bound) resource
	// block. Real resource bodies (even OCI's more elaborate ones, e.g.
	// nested `ingress_security_rules` / `lifecycle` blocks) rarely nest
	// more than 3-4 levels deep.
	DefaultMaxBlockDepth = 50
)

// DefaultForceReplacementAttrs is the built-in, provider-agnostic
// replacement-forcing attribute name heuristic (acceptance criterion 2 / PRD
// R18) applied when Config.ForceReplacementAttrs is nil. plandiff is
// credential-free and has no provider schema to consult (that is precisely
// what "static" means here), so it cannot know, in general, which attribute
// changes force Terraform to destroy-and-recreate a resource — that varies
// per resource type and provider. Instead it flags a resource as
// ForcesReplacement when ANY of these conventionally-immutable,
// provider-spanning attribute names changed value: physical/placement
// attributes that are essentially universally immutable once a resource is
// created, across virtually every major cloud provider's Terraform
// resources. This is a documented, intentionally conservative heuristic
// (it will under-flag provider/resource-specific ForceNew attributes it
// doesn't know about) rather than a complete schema-aware analysis — see
// Engine.Diff's doc and resource_delta.go's flagsReplacement.
var DefaultForceReplacementAttrs = []string{"availability_domain", "availability_zone"}

// Config bounds the plandiff Engine's resource usage and its
// replacement-forcing heuristic. Every field's zero value means "use the
// conservative default" — see Resolved. An explicitly negative numeric field
// is always rejected, even though the corresponding zero value would have
// been resolved to a default — zero means "unset," not "no bound." A nil
// ForceReplacementAttrs means "use DefaultForceReplacementAttrs"; there is no
// way to explicitly request an empty list, mirroring the same
// zero-means-default limitation every numeric field here already has.
type Config struct {
	// ParseTimeout bounds a single Parser.Parse call.
	ParseTimeout time.Duration
	// ConcurrencyCap bounds how many parses may run concurrently.
	ConcurrencyCap int
	// MaxUnifiedBytes bounds the emitted unified diff text (passed through
	// to manifestdiff.Params.MaxUnifiedBytes).
	MaxUnifiedBytes int
	// CacheEntries bounds the in-memory LRU cache's entry count.
	CacheEntries int
	// MaxMaterializedBytes bounds the total bytes a single stack/module
	// subtree materialization may write to disk.
	MaxMaterializedBytes int64
	// MaxMaterializedFiles bounds the file count a single stack/module
	// subtree materialization may write.
	MaxMaterializedFiles int
	// MaxMaterializedDepth bounds tree recursion depth during
	// materialization.
	MaxMaterializedDepth int
	// MaxMaterializedNodes bounds the total number of tree entries (files
	// and directories combined) a single stack/module subtree
	// materialization may visit.
	MaxMaterializedNodes int
	// MaterializeTimeout bounds a single PlanRepo.MaterializeSubtreeBounded
	// call.
	MaterializeTimeout time.Duration
	// MaterializeConcurrencyCap bounds how many materializations may run
	// concurrently, independently of ConcurrencyCap.
	MaterializeConcurrencyCap int
	// MaxBlockDepth bounds a single resource body's nested-HCL-block
	// recursion depth (see DefaultMaxBlockDepth).
	MaxBlockDepth int
	// ForceReplacementAttrs is the replacement-forcing attribute name
	// heuristic (see DefaultForceReplacementAttrs). nil means "use the
	// default list."
	ForceReplacementAttrs []string
}

// Resolved returns a copy of c with every zero field replaced by its
// conservative default, after validating that no numeric field is
// explicitly negative.
func (c Config) Resolved() (Config, error) {
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c.withDefaults(), nil
}

// validate rejects a Config with any explicitly negative numeric field. Zero
// is exempt — it is the "use the default" sentinel.
func (c Config) validate() error {
	switch {
	case c.ParseTimeout < 0:
		return fmt.Errorf("plandiff: ParseTimeout must not be negative, got %v", c.ParseTimeout)
	case c.ConcurrencyCap < 0:
		return fmt.Errorf("plandiff: ConcurrencyCap must not be negative, got %d", c.ConcurrencyCap)
	case c.MaxUnifiedBytes < 0:
		return fmt.Errorf("plandiff: MaxUnifiedBytes must not be negative, got %d", c.MaxUnifiedBytes)
	case c.CacheEntries < 0:
		return fmt.Errorf("plandiff: CacheEntries must not be negative, got %d", c.CacheEntries)
	case c.MaxMaterializedBytes < 0:
		return fmt.Errorf("plandiff: MaxMaterializedBytes must not be negative, got %d", c.MaxMaterializedBytes)
	case c.MaxMaterializedFiles < 0:
		return fmt.Errorf("plandiff: MaxMaterializedFiles must not be negative, got %d", c.MaxMaterializedFiles)
	case c.MaxMaterializedDepth < 0:
		return fmt.Errorf("plandiff: MaxMaterializedDepth must not be negative, got %d", c.MaxMaterializedDepth)
	case c.MaxMaterializedNodes < 0:
		return fmt.Errorf("plandiff: MaxMaterializedNodes must not be negative, got %d", c.MaxMaterializedNodes)
	case c.MaterializeTimeout < 0:
		return fmt.Errorf("plandiff: MaterializeTimeout must not be negative, got %v", c.MaterializeTimeout)
	case c.MaterializeConcurrencyCap < 0:
		return fmt.Errorf("plandiff: MaterializeConcurrencyCap must not be negative, got %d", c.MaterializeConcurrencyCap)
	case c.MaxBlockDepth < 0:
		return fmt.Errorf("plandiff: MaxBlockDepth must not be negative, got %d", c.MaxBlockDepth)
	}
	return nil
}

// withDefaults returns a copy of c with every zero field replaced by its
// conservative default.
func (c Config) withDefaults() Config {
	if c.ParseTimeout == 0 {
		c.ParseTimeout = DefaultParseTimeout
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
	if c.MaxBlockDepth == 0 {
		c.MaxBlockDepth = DefaultMaxBlockDepth
	}
	if c.ForceReplacementAttrs == nil {
		// A defensive copy, never the shared package-level slice itself: a
		// caller that mutated a resolved Config's ForceReplacementAttrs in
		// place (e.g. c.ForceReplacementAttrs[0] = "x") would otherwise
		// corrupt DefaultForceReplacementAttrs' backing array for every
		// other Engine/Config in the process, including ones already
		// constructed — an exported mutable package-level variable is
		// exactly the hazard this guards against.
		c.ForceReplacementAttrs = append([]string(nil), DefaultForceReplacementAttrs...)
	}
	return c
}
