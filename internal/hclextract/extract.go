package hclextract

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// hclFilename is a synthetic filename handed to the HCL parser. It never
// touches the filesystem -- content always comes from git blob bytes -- and
// only affects diagnostic message text.
const hclFilename = "tracked.hcl"

// extract parses content as HCL and resolves e.path against it. Absent input
// (nil/empty content, per the FieldExtractor contract) and a path that
// matches nothing both yield Present=false with a nil error -- "not found"
// is not a failure. A non-nil error is reserved for genuinely unparseable
// HCL, so callers (the poller) can classify and skip just that file without
// losing the rest of the tracker's cycle.
func (e *Extractor) extract(content []byte) (domain.TrackedField, error) {
	if len(content) == 0 {
		return domain.TrackedField{Present: false}, nil
	}

	file, diags := hclsyntax.ParseConfig(content, hclFilename, hcl.InitialPos)
	if diags.HasErrors() {
		return domain.TrackedField{}, fmt.Errorf("hclextract: parse: %s", diags.Error())
	}

	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		// hclsyntax.ParseConfig always yields a *hclsyntax.Body on success;
		// this is defensive, not reachable in practice.
		return domain.TrackedField{}, fmt.Errorf("hclextract: unexpected body type %T", file.Body)
	}

	expr, found := resolveBody(body, e.path)
	if !found {
		return domain.TrackedField{Present: false}, nil
	}

	return domain.TrackedField{Value: renderExpr(expr, content), Present: true}, nil
}

// resolveBody walks segments against body, matching each step against either
// a nested block (type + labels) or an attribute (recursing into an
// object-constructor expression when segments remain). It returns
// found=false -- never an error -- when nothing matches, treating an absent
// path as normal per the FieldExtractor contract.
func resolveBody(body *hclsyntax.Body, segments []string) (hclsyntax.Expression, bool) {
	if len(segments) == 0 {
		return nil, false
	}
	head, rest := segments[0], segments[1:]

	if attr, ok := body.Attributes[head]; ok {
		if len(rest) == 0 {
			return attr.Expr, true
		}
		return resolveObjectExpr(attr.Expr, rest)
	}

	for _, block := range body.Blocks {
		if block.Type != head {
			continue
		}
		nLabels := len(block.Labels)
		if len(rest) < nLabels {
			continue
		}
		if !labelsMatch(block.Labels, rest[:nLabels]) {
			continue
		}
		remaining := rest[nLabels:]
		if len(remaining) == 0 {
			// The path resolves to a block itself, not a scalar attribute.
			// Blocks are not a supported leaf value -- treat as absent.
			return nil, false
		}
		return resolveBody(block.Body, remaining)
	}

	return nil, false
}

// labelsMatch reports whether a block's labels exactly equal the next
// len(labels) path segments, in order.
func labelsMatch(labels, segments []string) bool {
	for i, label := range labels {
		if segments[i] != label {
			return false
		}
	}
	return true
}

// resolveObjectExpr indexes into an object-constructor expression
// (e.g. `{ source = "...", version = "..." }`, as required_providers entries
// are written) by successive static keys. Only statically-known keys (bare
// identifiers or literal strings) are matched; a dynamic key never matches.
func resolveObjectExpr(expr hclsyntax.Expression, segments []string) (hclsyntax.Expression, bool) {
	obj, ok := expr.(*hclsyntax.ObjectConsExpr)
	if !ok || len(segments) == 0 {
		return nil, false
	}
	head, rest := segments[0], segments[1:]

	for _, item := range obj.Items {
		key, ok := staticObjectKey(item.KeyExpr)
		if !ok || key != head {
			continue
		}
		if len(rest) == 0 {
			return item.ValueExpr, true
		}
		return resolveObjectExpr(item.ValueExpr, rest)
	}
	return nil, false
}

// staticObjectKey extracts a literal string key from an object-constructor
// item's key expression. hclsyntax.ObjectConsKeyExpr.Value already
// special-cases bare identifiers as literal keys (HCL's documented
// behavior), so calling Value with a nil EvalContext resolves both bare
// identifier keys and quoted string-literal keys without needing any
// variables in scope; a dynamic key (e.g. built from a reference or
// interpolation) fails and is correctly treated as unmatched.
func staticObjectKey(keyExpr hclsyntax.Expression) (string, bool) {
	v, diags := keyExpr.Value(nil)
	if diags.HasErrors() || v.IsNull() || v.Type() != cty.String {
		return "", false
	}
	return v.AsString(), true
}

// renderExpr resolves expr to its tracked string form. A pure literal (no
// variable/function references) is evaluated and stringified. Anything else
// -- a traversal, interpolation, function call, conditional, etc. -- is an
// HCL expression rather than a literal value, and per the extraction
// contract is captured as its original source text (sliced from the
// original file bytes via the expression's own byte range), not a resolved
// value.
func renderExpr(expr hclsyntax.Expression, src []byte) string {
	v, diags := expr.Value(nil)
	if !diags.HasErrors() && !v.IsNull() && v.Type().IsPrimitiveType() {
		return ctyPrimitiveToString(v)
	}
	return strings.TrimSpace(string(expr.Range().SliceBytes(src)))
}

// ctyPrimitiveToString formats a primitive cty.Value (string/number/bool) as
// the plain string TrackedField carries, matching the gojq extractor's
// fmt.Sprintf("%v", v) convention for scalars.
func ctyPrimitiveToString(v cty.Value) string {
	switch v.Type() {
	case cty.String:
		return v.AsString()
	case cty.Bool:
		return strconv.FormatBool(v.True())
	case cty.Number:
		bf := v.AsBigFloat()
		if bf.IsInt() {
			i, _ := bf.Int(new(big.Int))
			return i.String()
		}
		return bf.Text('f', -1)
	default:
		return fmt.Sprintf("%v", v)
	}
}
