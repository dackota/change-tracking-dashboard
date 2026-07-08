// Package plandiff (this file): the HCL-parsing seam Engine.Diff depends on,
// and its production implementation. defaultParser walks a materialized
// directory for *.tf/*.tofu files and extracts every top-level `resource
// "type" "name" { ... }` block into a Resource — no cloud SDK, no
// `terraform` binary, no state or credentials of any kind are ever touched
// (acceptance criterion 3): this file's only inputs are the bytes
// PlanRepo.MaterializeSubtreeBounded already wrote to local disk, and its
// only external dependency is the hashicorp/hcl/v2 parser used purely
// in-process.
package plandiff

import (
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// hclFilename is a synthetic filename handed to the HCL parser for
// diagnostic message text only; content always comes from a materialized
// on-disk file (see parseFile), which is itself sourced from git blob
// bytes -- this never touches the real filesystem for anything other than
// reading that already-bounded, already-written file.
const hclFilename = "resource.tf"

// errBlockDepthExceeded is returned by renderBody when a resource body's
// nested-block recursion exceeds Config.MaxBlockDepth -- classified as
// ExceededLimits by materializeAndParse, mirroring how
// gitsource.ErrMaterializeBoundsExceeded is classified.
var errBlockDepthExceeded = errors.New("plandiff: resource body nested-block recursion exceeded configured depth")

// Resource is a single Terraform resource block extracted from a directory
// of HCL files: its (Type, Name) address identity, its top-level attributes
// (for the replacement-forcing heuristic), and its full rendered body (for
// the manifestdiff unified-diff render).
type Resource struct {
	// Type is the resource type label (e.g. "oci_core_instance").
	Type string
	// Name is the resource's local name label (e.g. "web").
	Name string
	// Attrs maps each top-level attribute name to its rendered literal or
	// expression-text value (see renderExprText) -- used only by the
	// replacement-forcing heuristic (resource_delta.go), which only ever
	// looks at top-level attributes, mirroring Terraform's own ForceNew
	// attributes being top-level scalars in every provider this heuristic
	// targets.
	Attrs map[string]string
	// Body is the full deterministic rendering of the resource's attributes
	// and nested blocks (see renderBody) -- the "YAML" text manifestdiff
	// line-diffs.
	Body string
}

// Parser is the HCL-parsing seam Engine.Diff depends on. defaultParser{} is
// the production adapter; tests inject a fake to exercise timeout,
// concurrency, and cache behavior deterministically without real HCL files
// on disk.
type Parser interface {
	// Parse walks dir for Terraform source files and extracts every
	// resource block found. dir's content is already bounded by the
	// materialize step that populated it (Config's materialize ceilings);
	// Parse itself bounds only per-resource nested-block recursion depth
	// (Config.MaxBlockDepth), returning errBlockDepthExceeded when it is
	// exceeded.
	Parse(dir string) ([]Resource, error)
}

// defaultParser is the production Parser, walking the filesystem directly.
type defaultParser struct {
	maxBlockDepth int
}

// Parse implements Parser.
func (p defaultParser) Parse(dir string) ([]Resource, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if isTerraformFile(path) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("plandiff: walk %q: %w", dir, err)
	}
	// Deterministic file visit order: two materializations of the same
	// commit always parse in the same order, so a duplicate resource
	// identity (invalid HCL, but not this package's job to reject) resolves
	// its "last one wins" tie-break deterministically rather than depending
	// on filesystem readdir order.
	sort.Strings(files)

	var resources []Resource
	for _, f := range files {
		content, err := os.ReadFile(f) // #nosec G304 -- f comes only from filepath.WalkDir(dir), never caller input
		if err != nil {
			return nil, fmt.Errorf("plandiff: read %q: %w", f, err)
		}
		rs, err := parseFile(content, p.maxBlockDepth)
		if err != nil {
			return nil, fmt.Errorf("plandiff: parse %q: %w", f, err)
		}
		resources = append(resources, rs...)
	}
	return resources, nil
}

// isTerraformFile reports whether path is a Terraform source file that may
// contain resource blocks -- .tf and .tofu, deliberately excluding
// .terraform.lock.hcl (a provider-pin lockfile with no resource blocks;
// already tracked separately by the hclextract-backed FieldExtractor
// trackers, out of scope for the resource-level view this package renders).
func isTerraformFile(path string) bool {
	ext := filepath.Ext(path)
	return ext == ".tf" || ext == ".tofu"
}

// parseFile parses content as HCL and extracts every top-level `resource`
// block it declares. A parse-diagnostic error (malformed/unparseable HCL) is
// returned to the caller, which folds it into the whole-diff CouldNotRender
// classification -- mirroring chartrender.ReasonMalformedChart's "the whole
// unit failed, not a partial result" semantics, not the per-file
// skip-and-continue semantics hclextract's tracker-level extraction uses (a
// different context: a whole plan-diff needs a fully-resolved resource set
// on each side to be a trustworthy diff, not a partial one silently missing
// some resources).
func parseFile(content []byte, maxBlockDepth int) ([]Resource, error) {
	file, diags := hclsyntax.ParseConfig(content, hclFilename, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("plandiff: parse HCL: %s", diags.Error())
	}

	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		// hclsyntax.ParseConfig always yields a *hclsyntax.Body on success;
		// this is defensive, not reachable in practice.
		return nil, fmt.Errorf("plandiff: unexpected body type %T", file.Body)
	}

	var resources []Resource
	for _, block := range body.Blocks {
		if block.Type != "resource" || len(block.Labels) != 2 {
			continue
		}
		attrs, err := topLevelAttrs(block.Body, content)
		if err != nil {
			return nil, err
		}
		rendered, err := renderBody(block.Body, content, 1, maxBlockDepth)
		if err != nil {
			return nil, err
		}
		resources = append(resources, Resource{
			Type:  block.Labels[0],
			Name:  block.Labels[1],
			Attrs: attrs,
			Body:  rendered,
		})
	}
	return resources, nil
}

// topLevelAttrs renders only body's direct attributes (not nested blocks)
// into a flat name -> rendered-value map, for the replacement-forcing
// heuristic.
func topLevelAttrs(body *hclsyntax.Body, src []byte) (map[string]string, error) {
	attrs := make(map[string]string, len(body.Attributes))
	for name, attr := range body.Attributes {
		attrs[name] = renderExprText(attr.Expr, src)
	}
	return attrs, nil
}

// renderBody deterministically renders body's attributes (sorted by name)
// and nested blocks (in source order, recursing depth+1) into normalized
// text: "name = value" lines for attributes, "blockType \"labels\" { ... }"
// for nested blocks. depth starts at 1 for a resource's own top-level body;
// exceeding maxBlockDepth returns errBlockDepthExceeded rather than
// recursing further, so an adversarially deep (but byte-bound) HCL body can
// never exhaust the stack.
func renderBody(body *hclsyntax.Body, src []byte, depth, maxBlockDepth int) (string, error) {
	if depth > maxBlockDepth {
		return "", errBlockDepthExceeded
	}

	var b strings.Builder

	names := make([]string, 0, len(body.Attributes))
	for name := range body.Attributes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		b.WriteString(name)
		b.WriteString(" = ")
		b.WriteString(renderExprText(body.Attributes[name].Expr, src))
		b.WriteString("\n")
	}

	for _, block := range body.Blocks {
		b.WriteString(blockHeader(block))
		b.WriteString(" {\n")
		nested, err := renderBody(block.Body, src, depth+1, maxBlockDepth)
		if err != nil {
			return "", err
		}
		b.WriteString(indent(nested))
		b.WriteString("}\n")
	}

	return b.String(), nil
}

// blockHeader renders a nested block's type + labels, e.g. `lifecycle` or
// `ingress "TCP"`.
func blockHeader(block *hclsyntax.Block) string {
	if len(block.Labels) == 0 {
		return block.Type
	}
	quoted := make([]string, len(block.Labels))
	for i, l := range block.Labels {
		quoted[i] = strconv.Quote(l)
	}
	return block.Type + " " + strings.Join(quoted, " ")
}

// indent prefixes every line of text with two spaces, for nested-block
// rendering.
func indent(text string) string {
	lines := strings.SplitAfter(text, "\n")
	var b strings.Builder
	for _, line := range lines {
		if line == "" {
			continue
		}
		b.WriteString("  ")
		b.WriteString(line)
	}
	return b.String()
}

// renderExprText resolves expr to its tracked string form, mirroring
// hclextract's renderExpr/ctyPrimitiveToString convention exactly (kept as
// an independent, small implementation rather than an import: plandiff and
// hclextract are deliberately decoupled -- see this package's doc). A pure
// literal (no variable/function references) is evaluated and stringified;
// anything else (a traversal, interpolation, function call, conditional,
// etc.) is captured as its original source text, not a resolved value.
func renderExprText(expr hclsyntax.Expression, src []byte) string {
	v, diags := expr.Value(nil)
	if !diags.HasErrors() && !v.IsNull() && v.Type().IsPrimitiveType() {
		return ctyPrimitiveToString(v)
	}
	return strings.TrimSpace(string(expr.Range().SliceBytes(src)))
}

// ctyPrimitiveToString formats a primitive cty.Value (string/number/bool) as
// a plain string.
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
