package subtree

// FailureKind is the engine's own, domain-agnostic vocabulary for "the diff
// could not be computed". It is deliberately NOT a domain's Outcome Kind: the
// engine knows why it gave up in its own terms, and each Domain translates
// that into whatever classification its callers render (see Domain.Classify).
//
// Keeping the two vocabularies separate is what lets one engine serve domains
// whose Kind sets genuinely differ — chartdiff has an Unavailable that
// plandiff can never produce, and plandiff carries a resource-level Summary
// that means nothing for a chart.
type FailureKind int

const (
	// NoPriorVersion means the change commit is a root commit: FirstParent
	// reported gitsource.ErrNoParent, so there is no "old" side to diff.
	NoPriorVersion FailureKind = iota
	// ExceededLimits means a configured ceiling stopped the computation: a
	// materialize bound (gitsource.ErrMaterializeBoundsExceeded), the
	// materialize timeout, or the produce timeout.
	ExceededLimits
	// ProduceFailed means Domain.Produce itself returned an error. Cause
	// carries that error verbatim, unwrapped, so the Domain can classify its
	// own error vocabulary — the engine never inspects it. This is the only
	// Kind for which Cause is set, and the only one the engine does not log:
	// a Domain may classify some produce failures as expected outcomes it
	// does not want logged as errors.
	ProduceFailed
	// Internal means the engine failed before or around Produce for a reason
	// no Domain can classify: resolving the first parent, creating the
	// exclusive temp directory, or an unclassified materialize error. The
	// engine has already logged the cause server-side; Cause is not set, so a
	// Domain cannot accidentally leak it to a caller.
	Internal
)

// Failure describes why Engine.Diff could not produce a result, in the
// engine's own vocabulary, along with enough request context for a Domain to
// log or classify it. It is passed to Domain.Classify, which turns it into
// that domain's own Outcome.
type Failure struct {
	// Kind is why the engine gave up.
	Kind FailureKind
	// Cause is the error Domain.Produce returned. Set only when Kind is
	// ProduceFailed — the engine never populates it for its own failures, so
	// internal git/filesystem detail cannot reach a Domain and, through it, a
	// caller.
	Cause error
	// Side is which half of the diff failed: "old" or "new". Empty when the
	// failure is not side-specific (NoPriorVersion).
	Side string
	// Sha is the commit SHA of the failing side. Empty when Side is.
	Sha string
	// Req is the request being served, for log context.
	Req Request
}
