package extractor

import (
	"fmt"
	"strings"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
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

// Selection is a compiled FieldExtractor that knows which engine compiled it.
//
// The engine is not a separate fact a caller has to carry alongside the
// extractor and keep in step with it: resolving the engine and compiling the
// expression is one decision, made once, by Select. Callers that report which
// engine ran — a log line, an extract-failure metric — read it off the same
// value they extracted with, so the two can never disagree.
type Selection interface {
	FieldExtractor
	// Engine is the resolved engine that compiled this extractor: always a
	// concrete engine name ("jq" or "hcl"), never the empty auto-detect hint
	// a caller may have passed to Select.
	Engine() string
}

// selection is the Selection returned by Select.
type selection struct {
	engine string
	fe     FieldExtractor
}

func (s selection) Engine() string { return s.engine }

func (s selection) Extract(content []byte) (domain.TrackedField, error) {
	return s.fe.Extract(content)
}

// validateEngine reports whether engine is a legal value for a tracker's
// `engine` config field. The empty string (auto-detect: jq, or hcl when the
// glob selects it — see Select), "jq", and "hcl" are accepted. Any other
// value is rejected so a typo'd config fails fast instead of silently
// no-op'ing.
//
// This is not exported: Select applies it, and every caller with an engine to
// check also has an expression to compile, so there is no reason to ask the
// question separately — and one fewer way to ask it in the wrong order.
func validateEngine(engine string) error {
	switch engine {
	case "", engineJQ, engineHCL:
		return nil
	default:
		return fmt.Errorf("extractor: unrecognized engine %q (supported: %q, %q, or omit for auto-detection)", engine, engineJQ, engineHCL)
	}
}

// Select resolves the engine and compiles expr with it, returning the two as
// one Selection.
//
// engine is a hint, not a verdict: a non-empty value is used as given, and an
// empty value is auto-detected from glob's suffix — a glob ending in .tf,
// .tofu, or .terraform.lock.hcl selects hcl, anything else defaults to jq,
// matching every tracker's behavior before the hcl engine existed. Passing an
// empty glob is fine and simply means "no suffix to infer from".
//
// An unrecognized engine is rejected before expr is ever compiled.
func Select(engine, glob, expr string) (Selection, error) {
	if err := validateEngine(engine); err != nil {
		return nil, err
	}

	resolved := resolveEngine(engine, glob)

	var (
		fe  FieldExtractor
		err error
	)
	if resolved == engineHCL {
		fe, err = hclextract.New(expr)
	} else {
		fe, err = New(expr)
	}
	if err != nil {
		return nil, err
	}
	return selection{engine: resolved, fe: fe}, nil
}

// resolveEngine returns the concrete engine to compile with: an explicit
// (non-empty) engine unchanged, otherwise the engine glob's suffix implies.
func resolveEngine(engine, glob string) string {
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
