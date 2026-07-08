package plandiff_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/plandiff"
)

// FuzzDiff_RealParser_NeverPanics fuzzes the production HCL parser
// (defaultParser, exercised end to end through Engine.Diff, exactly as
// production code path would run it) with arbitrary byte content standing
// in for a hostile or malformed .tf file. Per this repo's testing standard,
// a pure/total-function contract over untrusted input gets a native fuzz
// target, not just hand-picked malformed-HCL examples: Diff must always
// return a classified Outcome and never panic, regardless of what bytes a
// tenant repository's .tf file contains.
func FuzzDiff_RealParser_NeverPanics(f *testing.F) {
	seeds := []string{
		"",
		"resource \"t\" \"n\" {}",
		"resource \"t\" \"n\" { a = 1 }",
		"resource \"t\" \"n\" {",             // unterminated
		"resource \"t\" \"n\" { a = ${{{ }",  // malformed interpolation
		"\x00\x01\x02binary garbage\xff\xfe", // non-UTF8 / binary
		"resource resource resource {}}}}}",
		"# just a comment\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, content string) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(content), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}

		repo := writeFilesRepo(map[string]map[string]string{
			"parent-sha": {"main.tf": ""},
			"commit-sha": {"main.tf": content},
		})

		engine, err := plandiff.NewEngine(plandiff.Config{}, nil)
		if err != nil {
			t.Fatalf("NewEngine: %v", err)
		}

		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Diff panicked on fuzzed content %q: %v", content, r)
			}
		}()

		outcome := engine.Diff(t.Context(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "commit-sha"})
		if !validKinds[outcome.Kind] {
			t.Fatalf("outcome.Kind = %q is not a valid classified Kind, for content %q", outcome.Kind, content)
		}
	})
}
