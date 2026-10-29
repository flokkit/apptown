package workflowexec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
)

// The steering artifact records what the agent was told. Failure to save it
// must prevent the invocation, rather than merely hide the evidence.
func TestALocalAgentRequiresItsSteeringArtifact(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "recorded"
		if failed {
			name = "recording fails"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, runID, stepID := admittedAgentStep(t, `{"prompt":"do it","output":{"kind":"report","name":"answer"}}`)
			pi := installFakePi(a, func(int, execution.Request) execution.Outcome {
				return execution.Outcome{Output: piSays("done")}
			})
			if failed {
				t.Cleanup(a.db.FailNextStatement("INSERT INTO artifacts"))
			}
			if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
				t.Fatal(err)
			}
			status, ev := stepStatus(t, a, stepID)
			artifacts := artifactsNamed(t, a, runID, "steering-build")
			if failed {
				reason, _ := ev["reason"].(string)
				if status != workflow.StatusError || !strings.Contains(reason, "steering") || len(pi.requests()) != 0 || len(artifacts) != 0 {
					t.Fatalf("unrecorded steering: status=%s evidence=%v invocations=%d artifacts=%d", status, ev, len(pi.requests()), len(artifacts))
				}
			} else if status != workflow.StatusPass || len(pi.requests()) != 1 || len(artifacts) != 1 {
				t.Fatalf("recorded control: status=%s evidence=%v invocations=%d artifacts=%d", status, ev, len(pi.requests()), len(artifacts))
			}
			if !failed {
				body, err := os.ReadFile(filepath.Join(pi.requests()[0].Workspace, ".apptown", "steering.md"))
				if err != nil {
					t.Fatal(err)
				}
				saved, err := a.work.Artifact(a.ctx, artifacts[0].ID)
				if err != nil || saved.Body != string(body) || saved.Ref != "sha256:"+deploy.ConfigHash(string(body)) {
					t.Fatalf("local steering receipt differs from invocation: %v %v", saved, err)
				}
			}
		})
	}
}

// Refusing at the worker boundary proves a recorded control reaches dispatch
// without running a program; failed receipt admission must never reach it.
type steeringReceiptWorker struct {
	executionfake.Tasks
	ensures int
	sent    execution.TaskSpec
}

func (d *steeringReceiptWorker) Capabilities() execution.Capabilities {
	return execution.Capabilities{Inputs: true, Results: true}
}
func (d *steeringReceiptWorker) AgentImage(context.Context) (execution.AgentImage, error) {
	return execution.AgentImage{Name: workerAgentImageName, Labels: map[string]string{"apptown.pi.version": "0.9"}}, nil
}
func (d *steeringReceiptWorker) Profile(context.Context) (execution.WorkerProfile, error) {
	return workerProfile, nil
}
func (d *steeringReceiptWorker) Ensure(_ context.Context, spec execution.TaskSpec) (execution.TaskObservation, error) {
	d.ensures++
	d.sent = spec
	return execution.TaskObservation{}, fmt.Errorf("test worker refused after dispatch: %w", execution.ErrUnsupported)
}
func (d *steeringReceiptWorker) Results(context.Context, execution.TaskRef) (map[string]execution.ResultFile, error) {
	return nil, nil
}

func TestAWorkerAgentRequiresItsSteeringArtifactBeforeReservation(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "recorded"
		if failed {
			name = "recording fails"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, runID, stepID := admittedAgentStep(t, `{"prompt":"do it","output":{"kind":"report","name":"answer"}}`)
			pi := installFakePi(a, func(int, execution.Request) execution.Outcome {
				return execution.Outcome{Output: piSays("must not run locally")}
			})
			driver := &steeringReceiptWorker{}
			a.tasks.Driver = driver
			a.agents.worker = &AgentWorker{Tasks: a.tasks}
			if failed {
				t.Cleanup(a.db.FailNextStatement("INSERT INTO artifacts"))
			}
			if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
				t.Fatal(err)
			}
			status, ev := stepStatus(t, a, stepID)
			step, err := workflow.ReadStepIn(t.Context(), a.db, stepID)
			if err != nil {
				t.Fatal(err)
			}
			invocations, err := a.tasks.Records.Invocations(t.Context(), stepID, step.Attempt)
			if err != nil {
				t.Fatal(err)
			}
			artifacts := artifactsNamed(t, a, runID, "steering-build")
			reason, _ := ev["reason"].(string)
			if len(pi.requests()) != 0 {
				t.Fatal("worker step ran locally")
			}
			if failed {
				if status != workflow.StatusError || !strings.Contains(reason, "steering") || driver.ensures != 0 || len(invocations) != 0 || len(artifacts) != 0 {
					t.Fatalf("unrecorded worker steering: status=%s evidence=%v dispatches=%d invocations=%d artifacts=%d", status, ev, driver.ensures, len(invocations), len(artifacts))
				}
			} else if status != workflow.StatusError || !strings.Contains(reason, "test worker refused") || driver.ensures != 1 || len(invocations) != 1 || len(artifacts) != 1 {
				t.Fatalf("recorded dispatch control: status=%s evidence=%v dispatches=%d invocations=%d artifacts=%d", status, ev, driver.ensures, len(invocations), len(artifacts))
			}
			if !failed {
				body := inputContent(t, inputsByPath(t, driver.sent)["/apptown/in/steering.md"])
				saved, err := a.work.Artifact(a.ctx, artifacts[0].ID)
				if err != nil || saved.Body != string(body) || saved.Ref != "sha256:"+deploy.ConfigHash(string(body)) {
					t.Fatalf("worker steering receipt differs from dispatched text: %v %v", saved, err)
				}
			}
		})
	}
}
