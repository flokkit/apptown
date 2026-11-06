package workflowexec

import (
	"encoding/json"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

// A failing suite needs the same input record as a passing one. Each command
// can resolve a different image or toolchain; unreadable observations stay unknown.
func TestTestsEvidenceRecordsEachCommandsActualInputs(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(map[bool]string{true: "observed", false: "unreadable"}[known], func(t *testing.T) {
			f, ch, folder := testsWorld(t, nil)
			commitAppDeclaration(t, folder, `app: {web: {build: ., public: true, test: ["first", "second"]}}`)
			tree := gittest.Run(t, folder, "rev-parse", "HEAD^{tree}")
			calls := 0
			f.checks = func(req execution.Request) execution.Outcome {
				calls++
				if !req.CaptureInputs {
					t.Error("inputs were not requested from the executor")
				}
				out := execution.Outcome{Termination: execution.Exited, ExitCode: 1}
				if known && calls == 1 {
					out.Inputs = &execution.RunInputs{ImageDigest: "sha256:actual", Toolchain: map[string][]string{"go": {"1.25.4"}}, Services: map[string]string{}}
				}
				return out
			}
			step := f.execute(t, testsRun(t, f, ch), "tests")
			ev := stepEvidence(t, step)
			in, ok := ev["inputs"].(map[string]any)
			if !ok {
				t.Fatalf("missing inputs: %s", step.Evidence)
			}
			if in["tree"] != tree || in["apptownVersion"] != "1.2.3-test" || step.Status != workflow.StatusFail {
				t.Fatalf("wrong receipt: %s", step.Evidence)
			}
			declarations, _ := in["declarations"].([]any)
			runs, _ := in["executions"].([]any)
			if len(declarations) != 2 || len(runs) != 2 {
				t.Fatalf("missing commands: %s", step.Evidence)
			}
			for i, raw := range runs {
				run := raw.(map[string]any)
				if known && i == 0 {
					b, _ := json.Marshal(run["toolchain"])
					if run["imageDigest"] != "sha256:actual" || string(b) != `{"go":["1.25.4"]}` {
						t.Fatalf("lost observed inputs: %v", run)
					}
				} else if run["imageDigest"] != "unknown" || run["toolchain"] != "unknown" {
					t.Fatalf("guessed inputs: %v", run)
				}
			}
		})
	}
}
