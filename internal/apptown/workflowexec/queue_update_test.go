package workflowexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
)

func queueCaller(f *taskAppTown) context.Context {
	return access.WithCaller(context.Background(), access.Caller{Verified: true, Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule", ProjectID: f.projectID}})
}
func queueRun(t *testing.T, f *taskAppTown, name, queue string) *workflow.Run {
	t.Helper()
	ctx := queueCaller(f)
	ch, err := f.work.Create(ctx, workflow.ChangeDraft{ProjectID: f.projectID, Type: "scheduled", Title: name, Queue: queue, Schedule: name})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.work.PrepareWorkspace(ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
	f.work.Stop()
	run, err := f.work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: name, Inputs: json.RawMessage(fmt.Sprintf(`{"level":%q}`, f.bound))})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	return run
}
func queueSteps(t *testing.T, f *taskAppTown, run *workflow.Run) map[string]*workflow.Step {
	t.Helper()
	snapshot, err := f.work.Snapshot(f.ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*workflow.Step{}
	for _, s := range snapshot.Steps {
		out[s.Name] = s
	}
	return out
}
func queueExecute(t *testing.T, f *taskAppTown, step *workflow.Step) {
	t.Helper()
	if step == nil {
		t.Fatal("step missing")
	}
	if err := f.work.ExecuteStep(queueCaller(f), step.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRepeatedAndSkippedProposalsStopBeforeTests(t *testing.T) {
	raw, err := stock.FS.ReadFile("janitor-update.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := schema.ParseWorkflow(raw)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := newNodeFixFixture(t, "updates", doc.Spec)
	f.commands.judge = updateJudgeFunc(func(_ context.Context, _ workflow.Source, _ check.Item, _ string) (workflow.CheckReceipt, error) {
		return workflow.CheckReceipt{Verdict: workflow.VerdictPass, Detail: "within scope"}, nil
	})
	// An unavailable test executor holds this proposal without starting repair;
	// repair and its first-fingerprint guarantee have their own case.
	f.checks = func(execution.Request) execution.Outcome {
		return execution.Outcome{Termination: execution.LaunchFailed, RunErr: errors.New("test executor unavailable")}
	}
	var first ids.ID
	for n := 0; n < 3; n++ {
		run := queueRun(t, f, "updates", "updates")
		steps := queueSteps(t, f, run)
		queueExecute(t, f, steps["update"])
		queueExecute(t, f, steps["routine"])
		if n == 0 {
			if status, _ := stepStatus(t, f, steps["routine"].ID); status != workflow.StatusPass {
				t.Fatalf("routine: %s; steps: %+v", status, queueSteps(t, f, run)["update"])
			}
			queueExecute(t, f, steps["tests"])
			first = run.ChangeID
		} else {
			steps = queueSteps(t, f, run)
			if steps["routine"].Status != workflow.StatusSkipped || steps["tests"].Status != workflow.StatusPending {
				t.Fatalf("duplicate reached tests: routine=%s %s, update=%s %s", steps["routine"].Status, steps["routine"].Evidence, steps["update"].Status, steps["update"].Evidence)
			}
			if n == 1 {
				if err := f.work.SkipUpdate(f.ctx, f.projectID, "updates", first); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

// The producer itself starts the retained attempt; the fixture never StartFixes it.
func TestThreeUninterruptedFailuresStopOnlyTheirQueue(t *testing.T) {
	for _, onWorker := range []bool{false, true} {
		t.Run(fmt.Sprintf("worker=%v", onWorker), func(t *testing.T) {
			t.Parallel()
			raw, err := stock.FS.ReadFile("janitor-update.yaml")
			if err != nil {
				t.Fatal(err)
			}
			doc, err := schema.ParseWorkflow(raw)
			if err != nil {
				t.Fatal(err)
			}
			f, _ := newNodeFixFixture(t, "updates", doc.Spec)
			installFakePi(f, func(int, execution.Request) execution.Outcome {
				return execution.Outcome{Termination: execution.Exited, ExitCode: 1, Output: piRefuses("producer failed")}
			})
			var worker *steeringReceiptWorker
			if onWorker {
				// The configured worker refuses dispatch after the producer claim.
				worker = &steeringReceiptWorker{}
				f.tasks.Driver = worker
				f.agents.worker = &AgentWorker{Tasks: f.tasks}
			}
			targets := []check.Finding{{Source: "osv", Identifiers: []string{"CVE-example"}, Package: &check.PackageRef{Name: "example", Version: "1.0.0", Ecosystem: "npm"}}}
			for n := 0; n < 3; n++ {
				ch, err := f.work.Create(queueCaller(f), workflow.ChangeDraft{ProjectID: f.projectID, Title: "update", Queue: "updates", Schedule: "updates"})
				if err != nil {
					t.Fatal(err)
				}
				if err := f.work.PrepareWorkspace(queueCaller(f), ch.ID); err != nil {
					t.Fatal(err)
				}
				f.work.Stop()
				targetJSON, _ := json.Marshal(targets)
				inputs, _ := json.Marshal(map[string]string{"targets": string(targetJSON), "level": "routine"})
				run, err := f.work.StartRun(queueCaller(f), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "updates", Inputs: inputs})
				if !errors.Is(err, workflow.ErrDispatchStopped) {
					t.Fatal(err)
				}
				queueExecute(t, f, queueSteps(t, f, run)["update"])
				// Read the retained JSON independently: the attempt API omits targets.
				var retained []byte
				if err := f.db.SQL().QueryRowContext(f.ctx, "SELECT targets FROM automated_changes WHERE id = $1", ch.ID).Scan(&retained); err != nil {
					t.Fatal(err)
				}
				var got []check.Finding
				if err := json.Unmarshal(retained, &got); err != nil || !reflect.DeepEqual(got, targets) {
					t.Fatalf("lost targets on attempt %d: %s (%v)", n+1, retained, err)
				}
				state, err := f.work.QueueState(f.ctx, f.projectID, "updates")
				if err != nil || state.Failed != n+1 || state.Stopped() != (n == 2) {
					t.Fatalf("attempt %d: %+v %v", n+1, state, err)
				}
			}
			other, err := f.work.QueueState(f.ctx, f.projectID, "security")
			if err != nil || other.Failed != 0 {
				t.Fatalf("other queue: %+v %v", other, err)
			}
			attempts, err := f.work.Attempts(f.ctx, f.projectID, "updates", 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(attempts) != 3 {
				t.Fatalf("attempts: got %d, want 3", len(attempts))
			}
			if onWorker && (worker.ensures != 3 || len(f.pi.requests()) != 0) {
				t.Fatalf("worker dispatches=%d, local invocations=%d", worker.ensures, len(f.pi.requests()))
			}
			for _, a := range attempts {
				if !a.Attempted || !a.Failed || a.Outcome != "held" {
					t.Fatalf("lost attempt: %+v", a)
				}
			}
		})
	}
}

// Scans have no running-image subjects in the command fixture.
type fixtureRunningImages struct{}

func (fixtureRunningImages) RunningImages(context.Context, ids.ID) ([]workflow.RunningImages, error) {
	return nil, nil
}

func TestProposalFingerprintBindsMainTreeTargetsAndAdoption(t *testing.T) {
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "propose", Uses: ScriptKind, With: map[string]any{"run": "true"}}}}}}
	f, _ := newNodeFixFixture(t, "fingerprint", spec)
	main, tree := "main-a", "tree-a"
	var same bool
	f.scriptRunner = func(task *workflow.Execution) {
		if _, err := task.Claim(); err != nil {
			t.Error(err)
			return
		}
		if err := task.StartFix(nil); err != nil {
			t.Error(err)
			return
		}
		var err error
		same, _, err = task.KeepProposal(main, tree, nil)
		if err != nil {
			t.Error(err)
			return
		}
		if same {
			task.Finish(workflow.EncodeOutcome(workflow.StatusSkipped, map[string]any{"reason": "same proposal"}, nil))
			if err := task.StopDuplicate("same proposal"); err != nil {
				t.Error(err)
			}
			return
		}
		task.Finish(workflow.EncodeOutcome(workflow.StatusFail, map[string]any{"reason": "fixture check failed"}, nil))
	}
	propose := func(inputs json.RawMessage, wantSame bool) {
		t.Helper()
		ch, err := f.work.Create(queueCaller(f), workflow.ChangeDraft{ProjectID: f.projectID, Type: "scheduled", Title: "proposal", Queue: "updates", Schedule: "fingerprint"})
		if err != nil {
			t.Fatal(err)
		}
		f.work.Stop()
		run, err := f.work.StartRun(queueCaller(f), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "fingerprint", Inputs: inputs})
		if !errors.Is(err, workflow.ErrDispatchStopped) {
			t.Fatal(err)
		}
		queueExecute(t, f, queueSteps(t, f, run)["propose"])
		if same != wantSame {
			t.Fatalf("same=%v want %v", same, wantSame)
		}
	}
	propose(nil, false)
	propose(nil, true)
	main = "main-b"
	propose(nil, false)
	tree = "tree-b"
	propose(nil, false)
	inputs := json.RawMessage(`{"targets":"[{\"fingerprint\":\"new-target\"}]"}`)
	propose(inputs, false)
	block, err := f.ways.CreateBlock(f.ctx, "fixture/update-context", "Update context.\n", "", taskAppTownAdministrator)
	if err != nil {
		t.Fatal(err)
	}
	body := "Update context with a new bound.\n"
	block, err = f.ways.EditBlock(f.ctx, block.ID, wow.BlockPatch{BodyMD: &body}, taskAppTownAdministrator)
	if err != nil {
		t.Fatal(err)
	}
	receipt := adoption.EmptyReceipt()
	receipt.Guidance = []adoption.ReceiptBlock{{Name: block.Name, BlockID: block.ID, Version: block.LatestVersion}}
	if err := f.db.InTx(t.Context(), func(tx *database.Tx) error {
		return adoption.RecordIn(f.ctx, tx, f.projectID, 3, adoption.AdoptionEdit, "new pinned guidance", receipt, taskAppTownAdministrator, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	propose(inputs, false)
	propose(inputs, true)
}
