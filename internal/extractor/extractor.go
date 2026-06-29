// Package extractor implements the Extractor module: given raw file bytes and
// a gojq expression, it extracts a scalar TrackedField value. The expression
// is compiled once at construction time for efficiency. This module is pure —
// no I/O, no side effects.
package extractor

import (
	"fmt"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/itchyny/gojq"
	"gopkg.in/yaml.v3"
)

// Extractor holds a compiled gojq query and runs it against file content to
// produce a scalar TrackedField result.
type Extractor struct {
	query *gojq.Query
}

// New compiles expr as a gojq expression and returns an Extractor ready to use.
// Returns a compile error if the expression is syntactically invalid.
func New(expr string) (*Extractor, error) {
	q, err := gojq.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("extractor: compile %q: %w", expr, err)
	}
	return &Extractor{query: q}, nil
}

// Extract runs the compiled expression against content (raw YAML/JSON bytes)
// and returns the scalar result. If the content is nil or the expression
// matches nothing (including selecting on a missing key), the returned field
// has Present=false. A non-nil error is returned only for evaluation failures
// that are not "no match" (e.g. type errors from the jq expression itself).
func (e *Extractor) Extract(content []byte) (domain.TrackedField, error) {
	if len(content) == 0 {
		return domain.TrackedField{Present: false}, nil
	}

	var data any
	if err := yaml.Unmarshal(content, &data); err != nil {
		return domain.TrackedField{}, fmt.Errorf("extractor: parse yaml: %w", err)
	}
	// gojq expects JSON-compatible data (map[string]any, not map[any]any).
	data = normalizeYAML(data)

	code, err := gojq.Compile(e.query)
	if err != nil {
		return domain.TrackedField{}, fmt.Errorf("extractor: compile code: %w", err)
	}

	iter := code.Run(data)
	v, ok := iter.Next()
	if !ok {
		// No output produced — expression matched nothing.
		return domain.TrackedField{Present: false}, nil
	}

	if err, isErr := v.(error); isErr {
		return domain.TrackedField{}, fmt.Errorf("extractor: eval: %w", err)
	}

	if v == nil {
		return domain.TrackedField{Present: false}, nil
	}

	// If the gojq output is a map (object), return a keyed TrackedField with each
	// value stringified — the same Sprintf approach as the scalar path uses.
	if m, ok := v.(map[string]any); ok {
		return buildKeyedField(m), nil
	}

	return domain.TrackedField{Value: fmt.Sprintf("%v", v), Present: true}, nil
}

// buildKeyedField converts a map[string]any gojq result into a keyed TrackedField.
// Each value is stringified with fmt.Sprintf("%v", v) to match the scalar path.
// The returned TrackedField has Present=true and a non-nil Map.
func buildKeyedField(m map[string]any) domain.TrackedField {
	result := make(map[string]string, len(m))
	for k, v := range m {
		result[k] = fmt.Sprintf("%v", v)
	}
	return domain.TrackedField{Present: true, Map: result}
}

// normalizeYAML converts map[any]any (what gopkg.in/yaml.v3 can produce for
// some edge cases) into map[string]any so gojq can process it. In practice
// yaml.v3 unmarshals to map[string]any directly for string keys, but we keep
// this helper for safety and correctness.
func normalizeYAML(v any) any {
	switch val := v.(type) {
	case map[any]any:
		out := make(map[string]any, len(val))
		for k, vv := range val {
			out[fmt.Sprintf("%v", k)] = normalizeYAML(vv)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, vv := range val {
			out[k] = normalizeYAML(vv)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, vv := range val {
			out[i] = normalizeYAML(vv)
		}
		return out
	default:
		return val
	}
}
