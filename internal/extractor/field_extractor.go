package extractor

import "github.com/dackota/change-tracking-dashboard/internal/domain"

// FieldExtractor is the seam the poller depends on to pull a TrackedField out
// of raw file content. Extract must treat "no match" (including a path/key
// that doesn't exist) as domain.TrackedField{Present: false}, nil — not an
// error. A non-nil error is reserved for genuine evaluation failures.
//
// The gojq-based Extractor is the only implementation today; an alternate
// backend (e.g. HCL, added in a later task) can satisfy this interface
// without the poller's poll/diff flow changing at all.
type FieldExtractor interface {
	Extract(content []byte) (domain.TrackedField, error)
}

// Compile-time assertion that the gojq-based Extractor satisfies FieldExtractor.
var _ FieldExtractor = (*Extractor)(nil)
