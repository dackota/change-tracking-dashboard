package domain

import "path"

// TenantPath is the directory a tracked Change lives in — the tenant chart
// directory for a Helm change, the stack/module directory for a Terraform
// one. It is the unit the on-demand diff endpoints materialize a subtree for,
// and the value their authorization gates compare against.
//
// It is a named type rather than a bare string because it is derived, not
// given: the same derivation has to happen when a change is rendered (into
// data-tenant-path) and again when the resulting request comes back and is
// authorized. Those two must agree exactly, and when they disagreed they did
// so silently — the round trip appeared to work while the documented
// forward-slash spelling was refused. Deriving through TenantPathOf makes
// that one expression in one place instead of one per call site.
type TenantPath string

// TenantPathOf derives c's TenantPath.
//
// path.Dir, never filepath.Dir: Change.FilePath is a git path and is
// forward-slash separated on every platform, whereas filepath.Dir rewrites
// the separator to "\" on Windows. Using filepath.Dir here makes the derived
// value disagree with the documented API spelling on Windows only — which is
// exactly the bug this type exists to make unrepeatable.
func TenantPathOf(c Change) TenantPath {
	return TenantPath(path.Dir(c.FilePath))
}

// ParseTenantPath interprets an untrusted, caller-supplied request parameter
// as a TenantPath.
//
// It deliberately does not clean, normalize or validate: the value is a
// lookup key, never a filesystem path. Nothing is opened from it — it is only
// ever compared for equality against a TenantPathOf a Change the store has
// already confirmed was ingested (see web.hasChangeAt). Normalizing here
// would make two spellings compare equal that TenantPathOf can never
// produce, widening the set of strings that pass the authorization gate
// rather than narrowing it.
func ParseTenantPath(s string) TenantPath {
	return TenantPath(s)
}

// String returns t's wire form — the spelling rendered into
// data-tenant-path and sent back as the "path" query parameter.
func (t TenantPath) String() string {
	return string(t)
}
