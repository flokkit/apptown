package check_test

import (
	"testing"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
)

// The timeout signal survives both deterministic evaluators and combining
// folder results, even when another folder passes and no output arrived.
func TestTimeoutSurvivesEvaluation(t *testing.T) {
	for _, kind := range []string{"sh", "source", "exec-image"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			run := func(execution.Request) execution.Outcome {
				calls++
				if calls == 1 {
					return execution.Outcome{Termination: execution.TimedOut}
				}
				return execution.Outcome{Termination: execution.Exited}
			}
			j := &check.Judge{ShImage: testShImage, Run: run, Scanners: check.ScannersFunc(func(string) (check.Scanner, bool) {
				return check.Scanner{Name: "check", Target: kind, Command: []string{"true"}}, true
			}), RunImage: func(string, []string, []string) execution.Outcome { return run(execution.Request{}) }}
			src := check.Source{Tree: t.TempDir(), Image: "sha256:app", Parts: []check.Part{{Name: "api", Folder: "api", Kinds: []string{check.KindBuild, check.KindContainer}, Public: true}, {Name: "web", Folder: "web", Kinds: []string{check.KindBuild}}}}
			d := sourced("rules", 1, "slow", "true", "high")
			d.AppliesTo = []string{"build"}
			var ev check.Evaluation
			if kind == "sh" {
				ev = j.EvaluateSh([]check.Declared{d}, src, check.Gate, "t")[0]
			} else {
				d.Sh = ""
				d.Evaluator = "tool"
				d.Tool = "check"
				ev = j.EvaluateTool(d, src, check.Gate, "t")
			}
			wantCalls := 2
			if kind == "exec-image" {
				wantCalls = 1
			}
			if calls != wantCalls || ev.Outcome != check.Error || !ev.TimedOut {
				t.Fatalf("lost timeout (%d calls): %+v", calls, ev)
			}
		})
	}
}
