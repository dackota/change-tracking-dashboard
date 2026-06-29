// validate.go contains the validation primitives that compile jq expressions
// and facet regexes. These call into the extractor and facet packages so the
// config module reuses the same compile paths the poller uses at runtime.
package config

import (
	"fmt"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/extractor"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/facet"
)

// domainTrackerType aliases domain.Tracker so parse.go can reference it
// without a separate import block. The domain package is imported here.
type domainTrackerType = domain.Tracker

// validateJQExpr compiles expr via extractor.New and wraps any error with
// a message that identifies tracker, file, and field.
func validateJQExpr(trackerIdx int, repo string, fileIdx int, glob string, fieldIdx int, name, expr string) error {
	_, err := extractor.New(expr)
	if err != nil {
		return fmt.Errorf("config: tracker[%d] (repo=%q), file[%d] (glob=%q), field[%d] (name=%q): invalid jq expr %q: %w",
			trackerIdx, repo, fileIdx, glob, fieldIdx, name, expr, err)
	}
	return nil
}

// validateFacetRegex compiles pattern via facet.NewExtractor and wraps any
// error with a message identifying the tracker.
func validateFacetRegex(trackerIdx int, repo, pattern string) error {
	_, err := facet.NewExtractor(pattern)
	if err != nil {
		return fmt.Errorf("config: tracker[%d] (repo=%q): invalid facetRegex %q: %w",
			trackerIdx, repo, pattern, err)
	}
	return nil
}
