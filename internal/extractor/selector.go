package extractor

import (
	"fmt"
	"strings"

	"github.com/dackota/change-tracking-dashboard/internal/hclextract"
)

// engineJQ and engineHCL are the engines implemented today. An empty engine
// value defaults to jq, preserving today's behavior for every existing
// tracker that predates the `engine` config field.
const (
	engineJQ  = "jq"
	engineHCL = "hcl"
)

// hclGlobSuffixes are the file-extension suffixes that route a tracker to the
// hcl engine when its `engine` config field is unset (auto-detection).
var hclGlobSuffixes = []string{".tf", ".tofu", ".terraform.lock.hcl"}

// ValidateEngine reports whether engine is a legal value for a tracker's
// `engine` config field. The empty string (defaults to jq, or to hcl when the
// glob auto-detects it — see InferEngine), "jq", and "hcl" are accepted. Any
// other value is rejected so a typo'd config fails fast instead of silently
// no-op'ing.
func ValidateEngine(engine string) error {
	switch engine {
	case "", engineJQ, engineHCL:
		return nil
	default:
		return fmt.Errorf("extractor: unrecognized engine %q (supported: %q, %q, or omit for auto-detection)", engine, engineJQ, engineHCL)
	}
}

// InferEngine resolves the engine a tracker actually runs: an explicit
// (non-empty) engine is returned unchanged. Otherwise it is inferred from
// glob's suffix — a glob ending in .tf, .tofu, or .terraform.lock.hcl selects
// hcl; anything else defaults to jq, matching every tracker's behavior before
// the hcl engine existed.
func InferEngine(engine, glob string) string {
	if engine != "" {
		return engine
	}
	for _, suffix := range hclGlobSuffixes {
		if strings.HasSuffix(glob, suffix) {
			return engineHCL
		}
	}
	return engineJQ
}

// Select returns the FieldExtractor for the given engine + expression. engine
// == "" is treated as jq (callers that need glob-based auto-detection must
// resolve it via InferEngine first — Select itself has no glob to infer
// from). An unrecognized engine is rejected before expr is ever compiled.
func Select(engine, expr string) (FieldExtractor, error) {
	if err := ValidateEngine(engine); err != nil {
		return nil, err
	}
	if engine == engineHCL {
		return hclextract.New(expr)
	}
	return New(expr)
}
