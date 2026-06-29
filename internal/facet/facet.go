// Package facet implements the Facet extractor: given a file path and a regex
// with named capture groups, it returns a map of facet key→value pairs.
// Missing or non-matching paths yield an empty map — never an error.
// This module is pure — no I/O, no side effects.
package facet

import (
	"fmt"
	"regexp"
)

// Extractor holds a compiled regex and extracts facet maps from file paths.
// A zero-value Extractor (e.g. from an empty pattern) always returns an empty
// facet map.
type Extractor struct {
	re *regexp.Regexp // nil when pattern is empty
}

// NewExtractor compiles the given regex pattern and returns an Extractor.
// An empty pattern is valid and always produces an empty facet map.
// A syntactically invalid pattern returns a compile error.
func NewExtractor(pattern string) (*Extractor, error) {
	if pattern == "" {
		return &Extractor{re: nil}, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("facet: compile pattern %q: %w", pattern, err)
	}
	return &Extractor{re: re}, nil
}

// ExtractFacets applies the compiled regex to filePath and returns a new map
// of all named capture groups that matched. If the pattern does not match, or
// there are no named groups, the returned map is empty (never nil).
func (e *Extractor) ExtractFacets(filePath string) map[string]string {
	if e.re == nil {
		return map[string]string{}
	}

	match := e.re.FindStringSubmatch(filePath)
	if match == nil {
		return map[string]string{}
	}

	names := e.re.SubexpNames()
	facets := make(map[string]string)
	for i, name := range names {
		if name == "" || i >= len(match) {
			continue
		}
		if match[i] != "" {
			facets[name] = match[i]
		}
	}
	return facets
}
