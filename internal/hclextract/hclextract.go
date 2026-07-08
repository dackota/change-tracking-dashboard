// Package hclextract implements the HCL extraction backend: given raw HCL
// (Terraform/OpenTofu) file bytes and a structural traversal expression
// (block type + labels -> attribute), it extracts a scalar TrackedField
// value. This module is pure -- no I/O, no side effects -- mirroring the
// gojq-based extractor package's shape so both satisfy the same
// extractor.FieldExtractor seam.
package hclextract

import "github.com/dackota/change-tracking-dashboard/internal/domain"

// Extractor holds a parsed traversal path and evaluates it against HCL file
// content to produce a scalar TrackedField result.
type Extractor struct {
	path []string
}

// New compiles expr as a structural traversal path and returns an Extractor
// ready to use. Returns an error if expr is not a well-formed path.
func New(expr string) (*Extractor, error) {
	segments, err := parsePath(expr)
	if err != nil {
		return nil, err
	}
	return &Extractor{path: segments}, nil
}

// Extract is implemented in extract.go.
func (e *Extractor) Extract(content []byte) (domain.TrackedField, error) {
	return e.extract(content)
}
