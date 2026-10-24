package check_test

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/check"
)

func TestValidateChecks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		checks  []check.Declaration
		wantErr string
	}{
		{"empty ok", nil, ""},
		{"sh inferred", []check.Declaration{{AppliesTo: repoWide, Name: "a", Sh: "true"}}, ""},
		{"llm inferred", []check.Declaration{{AppliesTo: repoWide, Name: "a", Rubric: "handlers stay thin"}}, ""},
		{"no name", []check.Declaration{{AppliesTo: repoWide, Sh: "true"}}, "name required"},
		{"dup name", []check.Declaration{{AppliesTo: repoWide, Name: "a", Sh: "true"}, {AppliesTo: repoWide, Name: "a", Sh: "true"}}, "duplicate"},
		{"sh without script", []check.Declaration{{AppliesTo: repoWide, Name: "a", Evaluator: "sh"}}, "requires a script"},
		{"llm without rubric", []check.Declaration{{AppliesTo: repoWide, Name: "a", Evaluator: "llm"}}, "requires a rubric"},
		{"bad evaluator", []check.Declaration{{AppliesTo: repoWide, Name: "a", Evaluator: "regex", Sh: "true"}}, "must be sh, llm, runtime or tool"},
		{"bad severity", []check.Declaration{{AppliesTo: repoWide, Name: "a", Sh: "true", Severity: "fatal"}}, "severity"},
	}
	for _, c := range cases {
		err := check.Validate(c.checks)
		if c.wantErr == "" && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: want error containing %q, got %v", c.name, c.wantErr, err)
		}
	}
	// defaults applied in place
	checks := []check.Declaration{{AppliesTo: repoWide, Name: "a", Sh: "true"}}
	_ = check.Validate(checks)
	if checks[0].Evaluator != "sh" || checks[0].Severity != "note" {
		t.Errorf("defaults not applied: %+v", checks[0])
	}
}

// Scope validation (§2.1): deterministic checks are always tree; a rubric
// defaults to diff; a tree rubric must name its files.
func TestCheckScopeValidation(t *testing.T) {
	t.Parallel()
	bad := []check.Declaration{{AppliesTo: repoWide, Name: "x", Evaluator: "sh", Sh: "true", Scope: "diff"}}
	if err := check.Validate(bad); err == nil || !strings.Contains(err.Error(), "always tree-scoped") {
		t.Fatalf("sh with scope diff must be refused, got %v", err)
	}
	bad = []check.Declaration{{AppliesTo: repoWide, Name: "y", Evaluator: "llm", Rubric: "r", Scope: "tree"}}
	if err := check.Validate(bad); err == nil || !strings.Contains(err.Error(), "files") {
		t.Fatalf("tree rubric without files must be refused, got %v", err)
	}
	ok := []check.Declaration{
		{AppliesTo: repoWide, Name: "a", Evaluator: "llm", Rubric: "r"},
		{AppliesTo: repoWide, Name: "b", Evaluator: "llm", Rubric: "r", Scope: "tree", Files: []string{"README.md"}},
		{AppliesTo: repoWide, Name: "c", Tool: "semgrep"},
		{AppliesTo: repoWide, Name: "d", Sh: "true"},
	}
	if err := check.Validate(ok); err != nil {
		t.Fatal(err)
	}
	want := []string{check.ScopeDiff, check.ScopeTree, check.ScopeTree, check.ScopeTree}
	for i, c := range ok {
		if c.Scope != want[i] || check.Scope(c) != want[i] {
			t.Errorf("%s: scope %q, want %q", c.Name, c.Scope, want[i])
		}
	}
	if check.JudgedWhere(ok[0]) != "at landing only" || check.JudgedWhere(ok[1]) != "at landing and in every review" {
		t.Errorf("judgedWhere wording: %q / %q", check.JudgedWhere(ok[0]), check.JudgedWhere(ok[1]))
	}
}
