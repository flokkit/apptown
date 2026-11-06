package workflowexec

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/stock"
)

// Keep the stock work/tests/accept sequence. Preview, guidance and report review
// have separate fixtures; this fixture replaces only those unrelated steps and
// the model, while normal dispatch, Git, test execution and rework remain real.
func TestStockChangeTestsRepairBeforeAcceptanceAndStopAtTheRoundBound(t *testing.T) {
	for _, repair := range []bool{true, false} {
		t.Run(fmt.Sprintf("repair=%t", repair), func(t *testing.T) {
			t.Parallel()
			raw, err := stock.FS.ReadFile("quick-change.yaml")
			if err != nil {
				t.Fatal(err)
			}
			doc, err := schema.ParseWorkflowStored(raw)
			if err != nil {
				t.Fatal(err)
			}
			stage := &doc.Spec.Stages[0]
			var entering []schema.Step
			for _, s := range stage.OnEnter {
				if s.Uses == AgentKind {
					s.With = map[string]any{"prompt": "make the change", "output": map[string]any{"kind": "change-report", "file": ".apptown/CHANGE.md"}}
					entering = append(entering, s)
				}
				if s.Uses == TestsKind {
					entering = append(entering, s)
				}
			}
			if len(entering) != 2 || entering[1].Uses != TestsKind {
				t.Fatal("stock change does not test the agent’s work before acceptance")
			}
			stage.OnEnter = entering
			stage.Steps = []schema.Step{{Name: "accept", Uses: "approval"}}
			f, _ := newTaskAppTown(t, "quick-change", doc.Spec)
			var checked atomic.Int32
			const failure = "declared command failed: npm test cannot find the test script"
			f.checks = func(req execution.Request) execution.Outcome {
				checked.Add(1)
				// The committed declaration, staged separately from the agent's workspace,
				// decides the check's answer. The real runner's exit status becomes evidence.
				body, err := os.ReadFile(filepath.Join(req.Workspace, "answer.txt"))
				if err != nil {
					t.Error(err)
				}
				if strings.TrimSpace(string(body)) == "fixed" {
					return execution.Outcome{Termination: execution.Exited, ExitCode: 0}
				}
				return execution.Outcome{Termination: execution.Exited, ExitCode: 1, Output: []byte(failure)}
			}
			pi := installFakePi(f, func(n int, req execution.Request) execution.Outcome {
				if n > 1 && !strings.Contains(strings.Join(req.Argv, " "), failure) {
					t.Errorf("agent did not receive test output: %v", req.Argv)
				}
				answer := "broken"
				if repair && n > 1 {
					answer = "fixed"
				}
				for path, body := range map[string]string{
					".apptown/app.yaml":  "app: {web: {build: ., public: true, test: [npm test]}}\n",
					".apptown/CHANGE.md": "Changed the app.\n",
					"answer.txt":         answer,
					"round.txt":          fmt.Sprint(n),
				} {
					if err := os.WriteFile(filepath.Join(req.Workspace, path), []byte(body), 0o644); err != nil {
						t.Error(err)
					}
				}
				gittest.Run(t, req.Workspace, "add", "answer.txt", "round.txt", ".apptown/app.yaml")
				gittest.Run(t, req.Workspace, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "agent work")
				piKeepsSession(t, req, "done")
				return execution.Outcome{Output: piSays("done")}
			})
			ch := f.beginChange(t)
			run, err := f.work.StartRun(f.ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "quick-change"})
			if err != nil {
				t.Fatal(err)
			}
			rounds := 3
			want := workflow.StatusFail
			if repair {
				rounds, want = 1, workflow.StatusPass
			}
			if !poll.Until(t.Context(), 90*time.Second, 10*time.Millisecond, func() bool {
				snap, err := f.work.Snapshot(f.ctx, run.ID, workflow.DetailView)
				if err != nil {
					return false
				}
				agentDone, testsDone := false, false
				for _, s := range snap.Steps {
					if s.Type == AgentKind {
						agentDone = s.Status == workflow.StatusPass && s.Attempt == int64(rounds+1)
					}
					if s.Type == TestsKind {
						testsDone = s.Status == want
					}
				}
				return agentDone && testsDone && len(runIDEvents(t, f, run.ID, "step.rework")) == rounds
			}) {
				t.Fatal("change did not reach its bounded handover")
			}
			f.work.Wait()
			if got := len(pi.requests()); got != rounds+1 || checked.Load() != int32(rounds+1) {
				t.Fatalf("agents=%d checks=%d; want %d", got, checked.Load(), rounds+1)
			}
			snap, err := f.work.Snapshot(f.ctx, run.ID, workflow.DetailView)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range snap.Steps {
				if s.Type == TestsKind {
					if s.Status != want || !strings.Contains(string(s.Evidence), `"commit"`) {
						t.Fatalf("change lost its test result: %+v", s)
					}
				}
				if s.Type == "approval" && s.Status != workflow.StatusPending {
					t.Fatalf("acceptance bypassed: %+v", s)
				}
			}
			if !repair && snap.WaitingOn.Kind != "check" {
				t.Fatalf("bound did not ask a person: %+v", snap.WaitingOn)
			}
		})
	}
}
