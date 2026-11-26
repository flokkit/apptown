package check_test

import (
	"github.com/flokkit/apptown/internal/check"
	"testing"
)

func TestAgentEvaluatorIdentityChangesWithItsQuestion(t *testing.T) {
	c := check.Declaration{Rubric: "root mise.toml exists", Scope: check.ScopeTree, Files: []string{"*.go"}}
	base := check.LLMEvaluatorVersion(c)
	for _, change := range []func(*check.Declaration){func(c *check.Declaration) { c.Rubric = "another question" }, func(c *check.Declaration) { c.EvidenceUse = "wording" }, func(c *check.Declaration) { c.Scope = check.ScopeDiff }, func(c *check.Declaration) { c.Files = []string{"*.ts"} }} {
		other := c
		change(&other)
		if check.LLMEvaluatorVersion(other) == base {
			t.Fatal("changed question retained evaluator identity")
		}
	}
}
func TestAFailureNeedsACitation(t *testing.T) {
	it := check.Item{Declared: check.Declared{Declaration: check.Declaration{Name: "required file"}}}
	if ev := check.Judgement(it, check.Verdict{Verdict: "fail"}, true, check.Gate); ev.Outcome != check.CannotAssess {
		t.Fatalf("uncited failure earned a verdict: %+v", ev)
	}
	if ev := check.Judgement(it, check.Verdict{Verdict: "fail", Evidence: "/src/mise.toml absent; ls -a /src"}, true, check.Gate); ev.Outcome != check.DoesNotConform {
		t.Fatalf("absence lookup cannot support a failure: %+v", ev)
	}
}

// Supplied verdicts cannot promote project-authored assertions to evidence.
func TestJudgementRequiresEvidenceBeyondProjectClaims(t *testing.T) {
	for _, citation := range []string{"PROJECT-NOTES.md:2", "AGENTS.md:7", "./PROJECT-NOTES.md:2 and docs/AGENTS.md:3",
		// Image tags and digests beside a claim are not files.
		"PROJECT-NOTES.md:4 (records the image digest sha256:1910abcdef)", "AGENTS.md:7 (image python:3.12.16-slim)", "PROJECT-NOTES.md:4 (python:3, sha256:1910)"} {
		for _, verdict := range []string{"pass", "fail", "unclear"} {
			it := check.Item{}
			got := check.Judgement(it, check.Verdict{Verdict: verdict, Evidence: citation}, true, check.Gate)
			if got.Outcome != check.CannotAssess || len(got.Findings) != 0 {
				t.Fatalf("%s %s: %+v", verdict, citation, got)
			}
		}
	}
	for _, tc := range []struct{ use, evidence string }{{"", "PROJECT-NOTES.md:2 and app.go:3"}, {"wording", "PROJECT-NOTES.md:2"},
		// A digest-only image move cites the changed Dockerfile line (demo, 26 Nov).
		{"", "/src/Dockerfile:1; /src/PROJECT-NOTES.md:4"}, {"", "PROJECT-NOTES.md:4 and Makefile:12-14."}} {
		it := check.Item{Declared: check.Declared{Declaration: check.Declaration{EvidenceUse: tc.use}}}
		if got := check.Judgement(it, check.Verdict{Verdict: "pass", Evidence: tc.evidence}, true, check.Gate); got.Outcome != check.Conforms {
			t.Fatalf("%+v: %+v", tc, got)
		}
	}
}
