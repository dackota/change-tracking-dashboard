// Package changeset (this file): the slug drift guard. It reads risk.go's own
// source to enumerate the Risk constants, rather than trusting a hand-written
// list that would need the same discipline it is meant to enforce. A test that
// checks a slice against a map only catches an author who updated one of the
// two; this catches an author who updated neither.
package changeset

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// riskConstantsInSource parses risk.go and returns the names and values of
// every declared constant of type Risk, as written in the source.
func riskConstantsInSource(t *testing.T) map[string]Risk {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "risk.go", nil, 0)
	if err != nil {
		t.Fatalf("parse risk.go: %v", err)
	}

	found := make(map[string]Risk)
	ast.Inspect(file, func(n ast.Node) bool {
		decl, ok := n.(*ast.GenDecl)
		if !ok || decl.Tok != token.CONST {
			return true
		}
		for _, spec := range decl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			// Only constants explicitly typed `Risk`, so the SemverBumpLevel
			// block in the same file is not swept up.
			ident, ok := vs.Type.(*ast.Ident)
			if !ok || ident.Name != "Risk" {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				// Strip the surrounding quotes from the literal.
				found[name.Name] = Risk(lit.Value[1 : len(lit.Value)-1])
			}
		}
		return true
	})
	return found
}

// TestRiskSlugs_CoverEveryDeclaredRiskConstant is the drift guard the risk
// filter depends on: every Risk constant declared in risk.go must have a slug
// in riskSlugs. Without it, adding a fifth class would compile, classify, and
// render its badge — while being silently unfilterable, because the request
// parser validates against RiskSlugs() and would simply never accept it.
//
// It reads the source rather than a maintained list on purpose: a list would
// need the same update discipline as the slug map itself, so an author who
// forgot both would sail through.
func TestRiskSlugs_CoverEveryDeclaredRiskConstant(t *testing.T) {
	t.Parallel()

	declared := riskConstantsInSource(t)

	if len(declared) == 0 {
		t.Fatal("no Risk constants found in risk.go — the guard has lost track of its subject (renamed file or changed declaration style), not a passing result")
	}

	for name, value := range declared {
		if _, ok := riskSlugs[value]; !ok {
			t.Errorf("Risk constant %s (%q) has no entry in riskSlugs — add one so the class is filterable via ?risk=<slug>", name, value)
		}
	}

	// The converse: a slug for a class that no longer exists is dead wire
	// surface that would accept a filter matching nothing.
	byValue := make(map[Risk]string, len(declared))
	for name, value := range declared {
		byValue[value] = name
	}
	for risk := range riskSlugs {
		if _, ok := byValue[risk]; !ok {
			t.Errorf("riskSlugs has an entry for %q, which is not a declared Risk constant — remove it", risk)
		}
	}
}
