package chartrender

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	releaseutil "helm.sh/helm/v4/pkg/release/v1/util"
)

// canonicalIndent is the indentation width used when re-serializing a
// document as canonical YAML, matching the 2-space style Kubernetes manifests
// conventionally use.
const canonicalIndent = 2

// Manifest is a single Kubernetes object extracted from a chart render.
type Manifest struct {
	// Kind, Namespace, and Name are the object's identity. They drive the
	// deterministic sort order within a Result and — downstream, in
	// manifestdiff — let a caller recognize "the same object" across two
	// renders to compute a manifests-changed count.
	Kind      string
	Namespace string
	Name      string
	// YAML is this object alone, re-serialized with canonical (alphabetical)
	// key ordering. gopkg.in/yaml.v3's encoder sorts map keys — same as
	// sigs.k8s.io/yaml's JSON round-trip did — so a chart author reordering
	// keys in the source template never shows up as a spurious diff
	// downstream. Unlike a JSON round-trip, decoding through yaml.v3 keeps
	// integers as int64/uint64 rather than float64, so integers above
	// float64's 2^53 exact-integer range survive re-serialization intact.
	YAML string
}

// Result is a successful, fully offline chart render, normalized into a
// deterministic manifest set. Two renders of the same chart directory and
// values produce a byte-identical Result — that determinism is what lets
// manifestdiff line-diff two manifest sets without noise from Helm's
// nondeterministic raw document or map-key ordering.
type Result struct {
	// Manifests is the normalized manifest set, sorted by
	// (Kind, Namespace, Name).
	Manifests []Manifest
}

// Normalized concatenates the manifest set into a single "---"-separated
// multi-document YAML stream, in the same (Kind, Namespace, Name) order as
// Manifests. It is the text manifestdiff line-diffs.
func (r *Result) Normalized() string {
	var b strings.Builder
	for i, m := range r.Manifests {
		if i > 0 {
			b.WriteString("---\n")
		}
		b.WriteString(m.YAML)
	}
	return b.String()
}

// documentIdentity is the subset of a Kubernetes object's fields normalize
// needs in order to sort and identify a rendered document.
type documentIdentity struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
}

// normalize splits raw — the concatenated manifest text a client-only Helm
// render produces — into a sorted, canonical Manifest set. Helm itself has
// already excluded hook resources and fully empty templates from raw (see
// action.Install.Run's renderResources); normalize additionally drops any
// residual bookkeeping-only document (e.g. a template that rendered to
// nothing but its "# Source:" comment) and strips that comment from every
// surviving document as a side effect of re-serializing through a generic
// interface{}, which discards YAML comments.
func normalize(raw string) ([]Manifest, error) {
	docs := releaseutil.SplitManifests(raw)

	manifests := make([]Manifest, 0, len(docs))
	for _, doc := range docs {
		m, ok, err := normalizeDocument(doc)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		manifests = append(manifests, m)
	}

	sortManifests(manifests)

	return manifests, nil
}

// normalizeDocument turns one raw, "# Source:"-commented YAML document into
// a Manifest. ok is false for a document that carries no Kubernetes object at
// all (blank, comment-only, or an empty mapping) and must be dropped rather
// than surfaced. A document that has real content but no kind — e.g. a stray
// mid-manifest "---" splitting one object's fields across two documents — is
// never dropped: normalizeDocument returns an error for it, since silently
// dropping real content would produce a truncated Result in violation of
// Render's "never a partial result" invariant.
func normalizeDocument(raw string) (m Manifest, ok bool, err error) {
	var obj interface{}
	if err := yaml.Unmarshal([]byte(raw), &obj); err != nil {
		return Manifest{}, false, fmt.Errorf("chartrender: parse rendered document: %w", err)
	}
	if isEmptyDocument(obj) {
		return Manifest{}, false, nil
	}

	var id documentIdentity
	if err := yaml.Unmarshal([]byte(raw), &id); err != nil {
		return Manifest{}, false, fmt.Errorf("chartrender: parse rendered document: %w", err)
	}
	if id.Kind == "" {
		return Manifest{}, false, fmt.Errorf("chartrender: rendered document has no kind: %v", obj)
	}

	canonical, err := marshalCanonical(obj)
	if err != nil {
		return Manifest{}, false, fmt.Errorf("chartrender: re-serialize rendered document: %w", err)
	}

	return Manifest{
		Kind:      id.Kind,
		Namespace: id.Metadata.Namespace,
		Name:      id.Metadata.Name,
		YAML:      canonical,
	}, true, nil
}

// isEmptyDocument reports whether obj is the decoded form of a document that
// legitimately carries no Kubernetes object: nil (blank or comment-only raw
// text) or an empty mapping (e.g. "{}"). Any other decoded value — including
// a non-empty mapping missing a kind — has real content and must not be
// treated as empty.
func isEmptyDocument(obj interface{}) bool {
	if obj == nil {
		return true
	}
	m, isMap := obj.(map[string]interface{})
	return isMap && len(m) == 0
}

// marshalCanonical re-serializes obj as YAML with sorted map keys (yaml.v3's
// encoder sorts them) and a fixed 2-space indent, so the result is
// byte-deterministic regardless of the key order or indentation the source
// template used.
func marshalCanonical(obj interface{}) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(canonicalIndent)
	if err := enc.Encode(obj); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// sortManifests sorts in place by (Kind, Namespace, Name), falling back to
// the canonical text itself so the order is fully deterministic even for
// two objects that happen to share identity.
func sortManifests(manifests []Manifest) {
	sort.Slice(manifests, func(i, j int) bool {
		a, b := manifests[i], manifests[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.YAML < b.YAML
	})
}
