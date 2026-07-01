package filter

import (
	"fmt"
	"strings"
)

// Parse builds a FilterSpec from request-style parameters (the same shape as
// url.Values: map[string][]string), restricted to the given set of allowed
// facet names. Every key in params must be present in allowed; parsing
// returns a new, independent FilterSpec and never mutates params or allowed.
//
// A value with a leading "-" (e.g. "-sbx") is an exclude; any other value is
// an include. A single facet may carry both includes and excludes.
func Parse(params map[string][]string, allowed map[string]struct{}) (FilterSpec, error) {
	includes := make(map[string]map[string]struct{})
	excludes := make(map[string]map[string]struct{})

	for key, values := range params {
		if _, ok := allowed[key]; !ok {
			return FilterSpec{}, fmt.Errorf("filter: invalid facet parameter")
		}
		for _, v := range values {
			if strings.HasPrefix(v, "-") {
				addValue(excludes, key, strings.TrimPrefix(v, "-"))
				continue
			}
			addValue(includes, key, v)
		}
	}

	return FilterSpec{includes: includes, excludes: excludes}, nil
}

// addValue records value under key in set, creating the inner set if needed.
func addValue(set map[string]map[string]struct{}, key, value string) {
	if set[key] == nil {
		set[key] = make(map[string]struct{})
	}
	set[key][value] = struct{}{}
}
