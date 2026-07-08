package plandiff_test

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"

	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/plandiff"
)

// validKinds is every Kind Diff is ever allowed to return -- the total-
// function contract acceptance criterion 4 demands.
var validKinds = map[plandiff.Kind]bool{
	plandiff.OK:             true,
	plandiff.NoPriorVersion: true,
	plandiff.CouldNotRender: true,
	plandiff.ExceededLimits: true,
}

// diffCase is one property-test input: a Request with adversarial field
// content (empty, embedded NUL bytes, oversized, unicode) paired with a
// PlanRepo/Parser behavior drawn from a small adversarial set (success,
// plain error, bounds-exceeded, no-parent, panic).
type diffCase struct {
	req          plandiff.Request
	behavior     string
	resourceBody string
}

var adversarialStrings = []string{
	"",
	"normal/path",
	"path/with\x00embedded/nul",
	"path with spaces",
	"路径/with/unicode/字符",
	stringsRepeat("a/", 500),
}

var repoBehaviors = []string{"ok", "error", "bounds-exceeded", "no-parent", "materialize-panic", "parse-panic"}

func stringsRepeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

// Generate implements quick.Generator.
func (diffCase) Generate(rnd *rand.Rand, size int) reflect.Value {
	req := plandiff.Request{
		RepoName:   adversarialStrings[rnd.Intn(len(adversarialStrings))],
		TenantPath: adversarialStrings[rnd.Intn(len(adversarialStrings))],
		CommitSha:  fmt.Sprintf("sha-%d", rnd.Int()),
	}
	behavior := repoBehaviors[rnd.Intn(len(repoBehaviors))]
	body := adversarialStrings[rnd.Intn(len(adversarialStrings))]
	return reflect.ValueOf(diffCase{req: req, behavior: behavior, resourceBody: body})
}

// TestDiff_NeverPanicsAndAlwaysReturnsExactlyOneClassifiedKind_Property
// asserts acceptance criterion 4 directly: for ANY Request content
// (including adversarial paths/shas) and ANY PlanRepo/Parser behavior
// (success, plain error, bounds-exceeded, root commit, or a panic
// originating in either the materialize or parse step), Engine.Diff never
// panics and always returns exactly one of the fixed set of classified
// Kinds -- never propagating a raw error or crashing the process. This
// subsumes the whole class of "what if the input/dependency is malformed in
// some way" cases a hand-picked example table can only sample from.
func TestDiff_NeverPanicsAndAlwaysReturnsExactlyOneClassifiedKind_Property(t *testing.T) {
	t.Parallel()

	property := func(c diffCase) bool {
		repo := &fakePlanRepo{
			firstParentFn: func(string) (string, error) {
				if c.behavior == "no-parent" {
					return "", gitsource.ErrNoParent
				}
				return "parent-sha", nil
			},
			materializeFn: func(string, string, string, gitsource.MaterializeBounds) error {
				switch c.behavior {
				case "bounds-exceeded":
					return gitsource.ErrMaterializeBoundsExceeded
				case "materialize-panic":
					panic("synthetic materialize panic")
				}
				return nil
			},
		}
		parser := &fakeParser{
			fn: func(_ int, _ string) ([]plandiff.Resource, error) {
				switch c.behavior {
				case "error":
					return nil, errUnexpected
				case "parse-panic":
					panic("synthetic parse panic")
				}
				return []plandiff.Resource{{Type: "t", Name: "n", Body: c.resourceBody}}, nil
			},
		}

		engine, err := plandiff.NewEngine(plandiff.Config{}, parser)
		if err != nil {
			t.Logf("NewEngine: %v", err)
			return false
		}

		var outcome plandiff.Outcome
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Logf("Diff panicked: %v (req=%+v behavior=%s)", r, c.req, c.behavior)
					outcome = plandiff.Outcome{Kind: "PANIC"}
				}
			}()
			outcome = engine.Diff(context.Background(), repo, c.req)
		}()

		return validKinds[outcome.Kind]
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 200}); err != nil {
		t.Error(err)
	}
}
