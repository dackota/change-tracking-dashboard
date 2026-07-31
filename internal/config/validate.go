// validate.go contains the validation primitives that compile jq expressions
// and facet regexes. These call into the extractor and facet packages so the
// config module reuses the same compile paths the poller uses at runtime.
package config

import (
	"fmt"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/extractor"
	"github.com/dackota/change-tracking-dashboard/internal/facet"
)

// domainTrackerType aliases domain.Tracker so parse.go can reference it
// without a separate import block. The domain package is imported here.
type domainTrackerType = domain.Tracker

// validateExpr compiles expr through the same extractor.Select seam the
// poller resolves its extractor from — engine explicit or, when unset,
// inferred from glob — so a jq tracker's expr is checked as jq and an hcl
// tracker's (explicit or glob-inferred) expr is checked as an HCL structural
// traversal path. Any compile error is wrapped with a message identifying
// tracker, file, and field.
//
// The error reports the configured engine and glob — what the operator
// actually wrote and can fix — rather than the resolved engine, which is a
// derived value and unavailable on the failure path anyway.
func validateExpr(trackerIdx int, repo string, fileIdx int, glob string, fieldIdx int, name, engine, expr string) error {
	if _, err := extractor.Select(engine, glob, expr); err != nil {
		return fmt.Errorf("config: tracker[%d] (repo=%q), file[%d] (glob=%q), field[%d] (name=%q, engine=%q): invalid expr %q: %w",
			trackerIdx, repo, fileIdx, glob, fieldIdx, name, engine, expr, err)
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
