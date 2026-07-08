package hclextract

import "fmt"

// parsePath splits a structural traversal expression into path segments.
// Segments are normally dot-separated (e.g. "module.vpc.source"); a segment
// that itself contains a "." (e.g. a lockfile provider address like
// "registry.terraform.io/hashicorp/google") is written in bracket-quote form
// ("provider[\"registry.terraform.io/hashicorp/google\"].version") so it is
// never split on its internal dots.
func parsePath(path string) ([]string, error) {
	if path == "" {
		return nil, fmt.Errorf("hclextract: empty traversal expression")
	}

	var segments []string
	i, n := 0, len(path)
	for i < n {
		switch path[i] {
		case '.':
			i++
		case '[':
			seg, next, err := parseBracketSegment(path, i)
			if err != nil {
				return nil, err
			}
			segments = append(segments, seg)
			i = next
		default:
			j := i
			for j < n && path[j] != '.' && path[j] != '[' {
				j++
			}
			if j == i {
				return nil, fmt.Errorf("hclextract: invalid traversal expression %q: empty segment at offset %d", path, i)
			}
			segments = append(segments, path[i:j])
			i = j
		}
	}

	if len(segments) == 0 {
		return nil, fmt.Errorf("hclextract: invalid traversal expression %q: no segments", path)
	}
	return segments, nil
}

// parseBracketSegment parses a ["literal"] bracket segment starting at
// path[start] == '['. It returns the unquoted literal and the index just
// past the closing ']'.
func parseBracketSegment(path string, start int) (segment string, next int, err error) {
	n := len(path)
	if start+1 >= n || path[start+1] != '"' {
		return "", 0, fmt.Errorf("hclextract: invalid traversal expression %q: expected '[\"' at offset %d", path, start)
	}
	j := start + 2
	for j < n && path[j] != '"' {
		j++
	}
	if j >= n || j+1 >= n || path[j+1] != ']' {
		return "", 0, fmt.Errorf("hclextract: invalid traversal expression %q: unterminated bracket segment starting at offset %d", path, start)
	}
	return path[start+2 : j], j + 2, nil
}
