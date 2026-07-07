package extractor

import "fmt"

// engineJQ is the only engine implemented today. An empty engine value
// defaults to it, preserving today's behavior for every existing tracker.
const engineJQ = "jq"

// ValidateEngine reports whether engine is a legal value for a tracker's
// `engine` config field. Only the empty string (defaults to jq) and "jq" are
// accepted right now. Any other value — including "hcl", reserved for a
// future engine that is not implemented yet — is rejected so a typo'd or
// premature config fails fast instead of silently no-op'ing.
func ValidateEngine(engine string) error {
	switch engine {
	case "", engineJQ:
		return nil
	default:
		return fmt.Errorf("extractor: unrecognized engine %q (supported: %q, or omit for the default)", engine, engineJQ)
	}
}

// Select returns the FieldExtractor for the given engine + expression. engine
// == "" defaults to the jq engine (today's only implementation, unchanged
// behavior). An unrecognized engine is rejected before expr is ever compiled.
func Select(engine, expr string) (FieldExtractor, error) {
	if err := ValidateEngine(engine); err != nil {
		return nil, err
	}
	return New(expr)
}
