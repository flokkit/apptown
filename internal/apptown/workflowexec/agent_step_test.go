package workflowexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
)

// Agent steps run through the agent runner as the daemon assembles it for
// this machine (the local executor), with Pi replaced by the test's fake:
// sessions, refusals, empty turns, evidence, reports and settlement are the
// runner's. How the root packages the CLI and the run token into an
// invocation stays with the root's tests (TestReviewInvocationsAllCarryCLIAndRunToken).

// admittedAgentStep admits a run whose one stage is the agent step "build"
// (config is its with:) and also, after it, the steps also, for a change
// created by the fixture's administrator; dispatch is stopped.
func admittedAgentStep(t *testing.T, config string, also ...schema.Step) (*taskAppTown, ids.ID, ids.ID) {
	t.Helper()
	return admittedAgentStepOn(t, func(f *taskAppTown) (*workflow.Change, error) {
		return f.work.Create(f.ctx, workflow.ChangeDraft{ProjectID: f.projectID, Title: "agent work"})
	}, config, also...)
}

// admittedAgentStepOn is admittedAgentStep for the change open opens.
func admittedAgentStepOn(t *testing.T, open func(*taskAppTown) (*workflow.Change, error), config string, also ...schema.Step) (*taskAppTown, ids.ID, ids.ID) {
	t.Helper()
	var with map[string]any
	if err := json.Unmarshal([]byte(config), &with); err != nil {
		t.Fatal(err)
	}
	f, _ := newTaskAppTown(t, "agent", schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
		{Name: "work", Steps: append([]schema.Step{{Name: "build", Uses: AgentKind, With: with}}, also...)}}})
	f.work.Stop()
	change, err := open(f)
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.work.StartRun(f.ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "agent"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := f.work.Snapshot(f.ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snapshot.Steps {
		if step.Name == "build" && step.Type == AgentKind && step.Status == workflow.StatusPending {
			return f, run.ID, step.ID
		}
	}
	t.Fatalf("pending agent step not materialized: %+v", snapshot.Steps)
	return nil, ids.Nil, ids.Nil
}

func stepStatus(t *testing.T, f *taskAppTown, stepID ids.ID) (workflow.StepState, map[string]any) {
	t.Helper()
	st, err := workflow.ReadStepIn(context.Background(), f.db, stepID)
	if err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	_ = json.Unmarshal(st.Evidence, &ev)
	return st.Status, ev
}

// runIDEvents are the events of kind of the run runID.
func runIDEvents(t *testing.T, f *taskAppTown, runID ids.ID, kind string) []map[string]any {
	t.Helper()
	run, err := workflow.ReadRunIn(context.Background(), f.db, runID)
	if err != nil {
		t.Fatal(err)
	}
	return runEvents(t, f, run, kind)
}

func mustJSONLine(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}

// piSays is a pi --mode json transcript with one assistant turn of text.
func piSays(text string) []byte {
	return mustJSONLine(map[string]any{"type": "message_end", "message": map[string]any{
		"role": "assistant", "model": "fake-model", "provider": "fake",
		"content": []map[string]any{{"type": "text", "text": text}},
		"usage":   map[string]any{"input": 10, "output": 5, "totalTokens": 15},
	}})
}

// piRefuses is the transcript a provider refusal leaves: an empty turn
// ended with stopReason "error" and the provider's words on it.
func piRefuses(msg string) []byte {
	return mustJSONLine(map[string]any{"type": "turn_end", "message": map[string]any{
		"role": "assistant", "content": []any{}, "stopReason": "error", "errorMessage": msg,
	}})
}

func sessionIDOf(req execution.Request) string {
	for i, x := range req.Argv {
		if x == "--session-id" && i+1 < len(req.Argv) {
			return req.Argv[i+1]
		}
	}
	return ""
}

// Session identity is per step ATTEMPT: a fresh dispatch gets step-<id>-a<n>
// where n counts this step's dispatches; a retry-with-continue resumes the
// PREVIOUS attempt's session (never the reviewer's, which shares the
// sessions dir).
func TestAgentSessionIDPerAttemptAndContinueResumesPrevious(t *testing.T) {
	t.Parallel()
	a, _, stepID := admittedAgentStep(t, `{"prompt":"do it"}`)
	// Each invocation leaves its session's transcript, as Pi does: a
	// continuation resumes the history its attempt kept.
	f := installFakePi(a, func(_ int, req execution.Request) execution.Outcome {
		piKeepsSession(t, req, "done")
		return execution.Outcome{Output: piSays("done")}
	})

	a.work.ExecuteStep(a.ctx, stepID)
	if got, want := sessionIDOf(f.requests()[0]), fmt.Sprintf("step-%d-a1", stepID); got != want {
		t.Fatalf("first dispatch: session %q, want %q", got, want)
	}

	// Second dispatch, fresh session: attempt 2.
	if _, err := a.work.RetryStep(a.ctx, workflow.RetryCommand{StepID: stepID}); err != nil {
		t.Fatal(err)
	}
	a.work.ExecuteStep(a.ctx, stepID)
	if got, want := sessionIDOf(f.requests()[1]), fmt.Sprintf("step-%d-a2", stepID); got != want {
		t.Fatalf("second dispatch: session %q, want %q", got, want)
	}
	if _, ev := stepStatus(t, a, stepID); ev["attempts"] != float64(2) {
		t.Fatalf("evidence must count attempts, got %v", ev["attempts"])
	}

	// Third dispatch as a retry-with-continue: resumes attempt 2's session
	// and sends only the feedback, not the task again.
	if _, err := a.work.RetryStep(a.ctx, workflow.RetryCommand{StepID: stepID, Mode: workflow.RetryContinue, Feedback: "fix the tests"}); err != nil {
		t.Fatal(err)
	}
	a.work.ExecuteStep(a.ctx, stepID)
	req := f.requests()[2]
	if got, want := sessionIDOf(req), fmt.Sprintf("step-%d-a2", stepID); got != want {
		t.Fatalf("continue: session %q, want the previous attempt %q", got, want)
	}
	if last := req.Argv[len(req.Argv)-1]; !strings.Contains(last, "fix the tests") || strings.Contains(last, "do it") {
		t.Fatalf("continue must send the feedback only, got %q", last)
	}
	st, _ := workflow.ReadStepIn(context.Background(), a.db, stepID)
	if strings.Contains(string(st.Config), "sendback") {
		t.Fatal("a consumed send-back note must be removed from the step config")
	}
	// Continuing twice must keep the actual conversation, rather than drift
	// to attempt 3's unused session ID. The start receipt records that identity.
	if _, err := a.work.RetryStep(a.ctx, workflow.RetryCommand{StepID: stepID, Mode: workflow.RetryContinue}); err != nil {
		t.Fatal(err)
	}
	a.work.ExecuteStep(a.ctx, stepID)
	if got, want := sessionIDOf(f.requests()[3]), fmt.Sprintf("step-%d-a2", stepID); got != want {
		t.Fatalf("second continue: session %q, want %q", got, want)
	}
}

// A provider refusal (billing, auth, quota) is infrastructure, not "the
// agent produced nothing": one call (no retry), status error, and the
// provider's own words as the reason.
func TestAgentProviderRefusedRecordsTheProvidersWords(t *testing.T) {
	t.Parallel()
	a, _, stepID := admittedAgentStep(t, `{"prompt":"do it"}`)
	f := installFakePi(a, func(int, execution.Request) execution.Outcome {
		return execution.Outcome{Output: piRefuses(`400 {"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`)}
	})
	a.work.ExecuteStep(a.ctx, stepID)
	if n := len(f.requests()); n != 1 {
		t.Fatalf("a refusal must not be retried, got %d calls", n)
	}
	status, ev := stepStatus(t, a, stepID)
	reason := fmt.Sprint(ev["reason"])
	if status != workflow.StatusError || !strings.HasPrefix(reason, "the model provider refused the request: 400 ") ||
		!strings.Contains(reason, "credit balance is too low") {
		t.Fatalf("want error with the provider's sentence, got %s %q", status, reason)
	}
	if ev["stderrTail"] == nil {
		t.Fatal("the raw transcript tail must be kept as evidence")
	}
}

// A step whose transcript could not be read whole says so in its evidence,
// naming the invocation, so its recorded usage is not read as the whole of
// what it spent; a step read whole carries no such mark.
func TestAgentEvidenceSaysWhenUsageIsIncomplete(t *testing.T) {
	t.Parallel()
	for _, broken := range []bool{true, false} {
		t.Run(fmt.Sprintf("broken=%t", broken), func(t *testing.T) {
			t.Parallel()
			a, _, stepID := admittedAgentStep(t, `{"prompt":"do it"}`)
			installFakePi(a, func(int, execution.Request) execution.Outcome {
				out := piSays("done")
				if broken {
					out = append([]byte(`{"type":"message_end","message":{"usage":{"input":`+"\n"), out...)
				}
				return execution.Outcome{Output: out}
			})
			a.work.ExecuteStep(a.ctx, stepID)
			status, ev := stepStatus(t, a, stepID)
			reason, marked := ev["usageIncomplete"].(string)
			if status != workflow.StatusPass || marked != broken || (broken && !strings.Contains(reason, "did not parse")) {
				t.Fatalf("usage completeness in evidence: %s %v", status, ev)
			}
		})
	}
}

// An empty turn (0 output tokens, no error) gets ONE automatic retry; the
// evidence carries the keys other code reads by name.
func TestAgentEmptyTurnRetriesOnceAndEvidenceKeysAreStable(t *testing.T) {
	t.Parallel()
	a, runID, stepID := admittedAgentStep(t, `{"prompt":"write the report","output":{"kind":"report","name":"r","file":".apptown/R.md"}}`)
	f := installFakePi(a, func(n int, req execution.Request) execution.Outcome {
		if n == 1 {
			return execution.Outcome{Output: []byte(`{"type":"agent_settled"}` + "\n")}
		}
		if err := os.WriteFile(filepath.Join(req.Workspace, ".apptown", "R.md"), []byte("# Report\n\nall done"), 0o644); err != nil {
			t.Fatal(err)
		}
		return execution.Outcome{Output: piSays("wrote the report")}
	})
	a.work.ExecuteStep(a.ctx, stepID)
	reqs := f.requests()
	if len(reqs) != 2 || !strings.HasSuffix(reqs[1].Name, "-r2") || sessionIDOf(reqs[0]) != sessionIDOf(reqs[1]) {
		t.Fatalf("want one retry in the same session under a distinct container name, got %d calls", len(reqs))
	}
	if retried := len(runIDEvents(t, a, runID, "step.retried")); retried != 1 {
		t.Fatalf("want one step.retried event, got %d", retried)
	}

	status, ev := stepStatus(t, a, stepID)
	if status != workflow.StatusPass {
		t.Fatalf("want pass, got %s %v", status, ev)
	}
	for _, k := range []string{"model", "modelSlot", "modelProfile", "piVersion", "steeringHash", "scmAccess",
		"glabVersion", "subagentsVersion", "provider", "usage", "output", "files", "durationMs", "queuedMs",
		"attempts", "executor", "guidanceHash", "skills", "resources"} {
		if _, ok := ev[k]; !ok {
			t.Errorf("evidence key %q missing (other code reads it by name)", k)
		}
	}
	if ev["output"] != "# Report\n\nall done" || ev["executor"] != execution.Docker || ev["model"] != "fake-model" {
		t.Fatalf("output must be the declared file's body: %v", ev)
	}
	// The declared output file becomes the typed artifact, once — not again
	// as a plain "file" artifact.
	artifacts, err := a.work.RunArtifacts(a.ctx, runID, workflow.ArtifactSelection{})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, artifact := range artifacts {
		if artifact.Name == "r" || artifact.Name == ".apptown/R.md" {
			kinds = append(kinds, artifact.Kind)
		}
	}
	if len(kinds) != 1 || kinds[0] != "report" {
		t.Fatalf("want exactly one typed 'report' artifact for the output file, got %v", kinds)
	}
}

func TestARestartRetryPublishesItsReplacementWork(t *testing.T) {
	t.Parallel()
	a, runID, stepID := admittedAgentStep(t, `{"prompt":"replace work"}`)
	snapshot, err := a.work.Snapshot(a.ctx, runID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	source, err := workflow.ReadSourceIn(context.Background(), a.db, workflow.RunRef{ID: runID, ProjectID: snapshot.Run.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	installFakePi(a, func(n int, req execution.Request) execution.Outcome {
		if err := os.WriteFile(filepath.Join(req.Workspace, "work.txt"), []byte(fmt.Sprintf("attempt %d\n", n)), 0o644); err != nil {
			t.Fatal(err)
		}
		return execution.Outcome{Output: piSays("done")}
	})
	if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
		t.Fatal(err)
	}
	first := gittest.Run(t, requireLocation(t)(source.Repository(a.dir)), "rev-parse", source.Branch)
	if _, err := a.work.RetryStep(a.ctx, workflow.RetryCommand{StepID: stepID, Mode: workflow.RetryRestart}); err != nil {
		t.Fatal(err)
	}
	if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
		t.Fatal(err)
	}
	want := gittest.Run(t, requireLocation(t)(source.Workspace(a.dir)), "rev-parse", "HEAD")
	got := gittest.Run(t, requireLocation(t)(source.Repository(a.dir)), "rev-parse", source.Branch)
	if got != want {
		t.Fatalf("restart result not published: hub=%s (old=%s), replacement=%s", got, first, want)
	}
}

// An agent step whose stage reviews its report against the diff is there
// to change the code: one that leaves the change empty fails, saying so
// with its own report, which is kept for the reviewer and a person (a bug
// it could not reproduce). A step whose report nobody reviews against the
// diff (research, a plan) passes without changing anything, and an
// implementing step that changed the code passes. A step that declares an
// empty result a complete answer (mayChangeNothing, a maintenance lane with
// nothing to update) passes with "nothing to change" and its report.
//
// An adoption Change (opened through the adoption admission, with its
// target) whose step changed nothing passes with "nothing to change" too,
// declared or not, and never as nothingToChange, which would close it
// unlanded: its landing judges the new checks and moves the project (soak
// 17). A Change merely typed "uplift" through the ordinary door has no
// target, and fails as any other.
func TestAnImplementingAgentStepThatChangesNothingFails(t *testing.T) {
	t.Parallel()
	const output = `{"prompt":"do it","output":{"kind":"change-report","file":".apptown/CHANGE.md"}}`
	const declared = `{"prompt":"do it","mayChangeNothing":true,"output":{"kind":"change-report","file":".apptown/CHANGE.md"}}`
	review := func(of string) schema.Step {
		return schema.Step{Name: "review", Uses: "agent/review", With: map[string]any{"of": of}}
	}
	ordinary := func(f *taskAppTown) (*workflow.Change, error) {
		return f.work.Create(f.ctx, workflow.ChangeDraft{ProjectID: f.projectID, Title: "agent work"})
	}
	typedUplift := func(f *taskAppTown) (*workflow.Change, error) {
		return f.work.Create(f.ctx, workflow.ChangeDraft{ProjectID: f.projectID, Type: "uplift", Title: "agent work"})
	}
	for _, c := range []struct {
		name, config string
		open         func(*taskAppTown) (*workflow.Change, error)
		review       schema.Step
		change       bool
		want         workflow.StepState
		nothing      bool
		adoption     bool
	}{
		{"reviewed, changed nothing", output, ordinary, review("change-report"), false, workflow.StatusFail, false, false},
		{"reviewed, changed the code", output, ordinary, review("change-report"), true, workflow.StatusPass, false, false},
		{"not reviewed against the diff", output, ordinary, review("other-report"), false, workflow.StatusPass, false, false},
		{"declared, changed nothing", declared, ordinary, review("change-report"), false, workflow.StatusPass, true, false},
		{"declared, changed the code", declared, ordinary, review("change-report"), true, workflow.StatusPass, false, false},
		{"adoption, changed nothing", output, openAdoptionChange, review("change-report"), false, workflow.StatusPass, false, true},
		{"adoption, declared, changed nothing", declared, openAdoptionChange, review("change-report"), false, workflow.StatusPass, false, true},
		{"adoption, changed the code", output, openAdoptionChange, review("change-report"), true, workflow.StatusPass, false, false},
		{"typed uplift without a target, changed nothing", output, typedUplift, review("change-report"), false, workflow.StatusFail, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			a, runID, stepID := admittedAgentStepOn(t, c.open, c.config, c.review)
			installFakePi(a, func(_ int, req execution.Request) execution.Outcome {
				if err := os.WriteFile(filepath.Join(req.Workspace, ".apptown", "CHANGE.md"), []byte("Not changed: the page named in the request does not exist."), 0o644); err != nil {
					t.Fatal(err)
				}
				if c.change {
					if err := os.WriteFile(filepath.Join(req.Workspace, "page.txt"), []byte("changed\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				return execution.Outcome{Output: piSays("done")}
			})
			if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
				t.Fatal(err)
			}
			status, ev := stepStatus(t, a, stepID)
			reason := fmt.Sprint(ev["reason"])
			if status != c.want || c.want == workflow.StatusFail && (!strings.HasPrefix(reason, "the step made no change") || !strings.Contains(reason, "does not exist")) {
				t.Fatalf("status %s, reason %q", status, reason)
			}
			if got := ev["nothingToChange"] == true; got != c.nothing || c.nothing && (!strings.HasPrefix(reason, "nothing to change: the ") || !strings.Contains(reason, "does not exist")) {
				t.Fatalf("nothingToChange %v, reason %q", ev["nothingToChange"], reason)
			}
			if got := strings.Contains(reason, "this adoption needs no code change"); got != c.adoption || c.adoption && (!strings.HasPrefix(reason, "nothing to change: the ") || !strings.Contains(reason, "does not exist")) {
				t.Fatalf("adoption %v, reason %q", c.adoption, reason)
			}
			run, err := workflow.ReadRunIn(context.Background(), a.db, runID)
			if err != nil {
				t.Fatal(err)
			}
			reports, err := workflow.ReadRunArtifactsIn(context.Background(), a.db, workflow.RunRef{ID: runID, ProjectID: run.ProjectID}, workflow.ArtifactSelection{Kinds: []string{"change-report"}, Latest: true})
			if err != nil || len(reports) != 1 || !strings.Contains(reports[0].Body, "does not exist") {
				t.Fatalf("the agent's report is not kept: %+v %v", reports, err)
			}
		})
	}
}

// What a local agent wrote is found by its files' modification times, which
// the operating system stamps: with AppTown's clock an hour ahead, a
// report the agent writes is still its output and its file.
//
// Kills: the harvest cutoff taken from AppTown's clock (every file written
// reads as older than the invocation and is left out).
func TestALocalAgentsReportIsHarvestedWhateverAppTownsClock(t *testing.T) {
	t.Parallel()
	a, _, stepID := admittedAgentStep(t, `{"prompt":"do it"}`)
	a.agents.clock = func() time.Time { return time.Now().Add(time.Hour) }
	installFakePi(a, func(_ int, req execution.Request) execution.Outcome {
		for name, body := range map[string]string{"report.md": "# what I did\n", "page.txt": "changed\n"} {
			if err := os.WriteFile(filepath.Join(req.Workspace, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return execution.Outcome{Output: piSays("")}
	})
	if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
		t.Fatal(err)
	}
	status, ev := stepStatus(t, a, stepID)
	if status != workflow.StatusPass || !strings.Contains(fmt.Sprint(ev["output"]), "what I did") || !strings.Contains(fmt.Sprint(ev["files"]), "report.md") {
		t.Fatalf("status %s, output %q, files %v", status, ev["output"], ev["files"])
	}
}

// A rework of a step on AppTown's own machine finds its earlier report
// where it declared it, even when the folder lost it, and is told to update
// it: the report describes the whole change, not the rework's note.
func TestAReworkUpdatesTheStepsEarlierReport(t *testing.T) {
	t.Parallel()
	a, runID, stepID := admittedAgentStep(t, `{"prompt":"do it","output":{"kind":"change-report","file":".apptown/CHANGE.md"}}`)
	snapshot, err := a.work.Snapshot(a.ctx, runID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	source, err := workflow.ReadSourceIn(context.Background(), a.db, workflow.RunRef{ID: runID, ProjectID: snapshot.Run.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(requireLocation(t)(source.Workspace(a.dir)), ".apptown", "CHANGE.md")
	var found, told bool
	installFakePi(a, func(n int, req execution.Request) execution.Outcome {
		if n == 1 {
			for name, body := range map[string]string{".apptown/CHANGE.md": "CHANGED page.txt: the first round\n", "page.txt": "first\n"} {
				if err := os.WriteFile(filepath.Join(req.Workspace, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			return execution.Outcome{Output: piSays("done")}
		}
		got, _ := os.ReadFile(filepath.Join(req.Workspace, ".apptown", "CHANGE.md"))
		found = string(got) == "CHANGED page.txt: the first round\n"
		told = strings.Contains(strings.Join(req.Argv, " "), "Your report from the earlier attempt is in .apptown/CHANGE.md. UPDATE it")
		return execution.Outcome{Output: piSays("done")}
	})
	if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(report); err != nil {
		t.Fatal(err)
	}
	if _, err := a.work.RetryStep(a.ctx, workflow.RetryCommand{StepID: stepID}); err != nil {
		t.Fatal(err)
	}
	if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
		t.Fatal(err)
	}
	if !found || !told {
		t.Fatalf("the rework found its earlier report: %v, was told to update it: %v", found, told)
	}
}

// ---- settling a paid agent's outcome ------------------------------------------

// A failed recording of a finished agent's outcome is recorded again, with
// the same outcome and artifacts; the agent is not invoked again for it.
// The fault is armed after the invocation returns,
// so only the settlement transaction can meet it.
func TestReviewAgentRetriesArtifactSettlementWithoutInvokingAgain(t *testing.T) {
	t.Parallel()
	a, runID, stepID := admittedAgentStep(t, `{"prompt":"do it","output":{"kind":"report","name":"answer"}}`)
	f := installFakePi(a, func(int, execution.Request) execution.Outcome {
		dbtest.FailNextStatement(t, a.db, "INSERT INTO artifacts")
		return execution.Outcome{Output: piSays("done")}
	})
	a.agents.Settle = workflow.Settlement{Wait: time.Millisecond}
	if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
		t.Fatal(err)
	}
	step, err := workflow.ReadStepIn(context.Background(), a.db, stepID)
	if err != nil {
		t.Fatal(err)
	}
	if step.Status != workflow.StatusPass || len(f.requests()) != 1 {
		t.Fatalf("transient artifact failure left status=%s, paid invocations=%d; expected settled pass after retry", step.Status, len(f.requests()))
	}
	// The retried settlement wrote the output once: the failed try rolled
	// back, and the fence admits one settlement of the attempt.
	if got := artifactsNamed(t, a, runID, "answer"); len(got) != 1 || got[0].Kind != "report" || got[0].Body != "done" {
		t.Fatalf("want the agent's one report artifact, got %+v", got)
	}
}

// When the outcome cannot be recorded within the bound, the step does not
// stay running after its agent finished: it is an error saying so, and the
// agent was invoked once. One try makes the one-shot fault persistent for
// the bound.
func TestAgentOutcomeThatCannotBeRecordedIsAnError(t *testing.T) {
	t.Parallel()
	a, runID, stepID := admittedAgentStep(t, `{"prompt":"do it","output":{"kind":"report","name":"answer"}}`)
	f := installFakePi(a, func(int, execution.Request) execution.Outcome {
		dbtest.FailNextStatement(t, a.db, "INSERT INTO artifacts")
		return execution.Outcome{Output: piSays("done")}
	})
	a.agents.Settle = workflow.Settlement{Tries: 1}
	if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
		t.Fatal(err)
	}
	status, ev := stepStatus(t, a, stepID)
	reason, _ := ev["reason"].(string)
	if status != workflow.StatusError || !strings.Contains(reason, "could not be recorded") || ev["unrecorded"] != "pass" || len(f.requests()) != 1 {
		t.Fatalf("want an error naming the unrecorded pass after one invocation, got %s %v (invocations %d)", status, ev, len(f.requests()))
	}
	if got := artifactsNamed(t, a, runID, "answer"); len(got) != 0 {
		t.Fatalf("an unrecorded pass leaves no artifacts, got %+v", got)
	}
}

func artifactsNamed(t *testing.T, a *taskAppTown, runID ids.ID, name string) []workflow.Artifact {
	t.Helper()
	all, err := a.work.RunArtifacts(a.ctx, runID, workflow.ArtifactSelection{})
	if err != nil {
		t.Fatal(err)
	}
	var out []workflow.Artifact
	for _, artifact := range all {
		if artifact.Name == name {
			out = append(out, artifact)
		}
	}
	return out
}

// A run whose step declared an empty result a complete answer and changed
// nothing ends when it would next ask a person and its report's review left
// nothing for one: the change is closed with the step's account and its
// approval is never asked. A review that leaves a finding (a deferred
// major), or a later step that changed the code, keeps it open for a person.
func TestARunWithNothingToChangeEndsWithoutAsking(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, findings, second string
		later, ends            bool
	}{
		{"reviewed clean", `[]`, "", false, true},
		{"the review left a finding", `["react 19 is a major: deferred"]`, "", false, false},
		{"another review left a finding", `[]`, `["react 19 is a major: deferred"]`, false, false},
		{"a later step changed the code", `[]`, "", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			also := []schema.Step{
				{Name: "review", Uses: ReviewKind, With: map[string]any{"of": "uplift-report"}},
				{Name: "accept", Uses: "approval", With: map[string]any{"approvers": []any{"role:project-approver"}}},
			}
			if c.second != "" {
				also = append([]schema.Step{{Name: "review-security", Uses: ReviewKind, With: map[string]any{"of": "uplift-report"}}}, also...)
			}
			if c.later {
				also = append([]schema.Step{{Name: "later", Uses: AgentKind, With: map[string]any{"prompt": "change it", "output": map[string]any{"kind": "uplift-report", "file": ".apptown/UPLIFT.md"}}}}, also...)
			}
			a, runID, stepID := admittedAgentStep(t, `{"prompt":"update","mayChangeNothing":true,"output":{"kind":"uplift-report","file":".apptown/UPLIFT.md"}}`, also...)
			installFakePi(a, func(n int, req execution.Request) execution.Outcome {
				write := func(name, body string) {
					if err := os.WriteFile(filepath.Join(req.Workspace, name), []byte(body), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				switch {
				case n == 1:
					write(".apptown/UPLIFT.md", "The project declares no dependencies.")
				case n == 2 && c.later:
					write("page.txt", "changed\n")
					write(".apptown/UPLIFT.md", "CHANGED page.txt")
				case n == 2 && c.second != "":
					write(".apptown/review.json", `{"verdict":"pass","summary":"a major waits","findings":`+c.second+`}`)
				default:
					write(".apptown/review.json", `{"verdict":"pass","summary":"nothing to update","findings":`+c.findings+`}`)
				}
				return execution.Outcome{Output: piSays("done")}
			})
			// The steps run in order: build, review-security, later, review.
			for _, name := range []string{"build", "review-security", "later", "review"} {
				id := stepID
				if name != "build" {
					id = pendingStepNamed(t, a, runID, name)
					if id.IsZero() {
						continue
					}
				}
				if err := a.work.ExecuteStep(a.ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.work.Advance(a.ctx, runID); err != nil {
				t.Fatal(err)
			}
			run, err := workflow.ReadRunIn(context.Background(), a.db, runID)
			if err != nil {
				t.Fatal(err)
			}
			change, err := workflow.ReadChangeIn(context.Background(), a.db, run.ChangeID)
			if err != nil {
				t.Fatal(err)
			}
			if ended := change.State == workflow.ChangeAbandoned && run.Status == workflow.RunAbandoned; ended != c.ends {
				t.Fatalf("change %s, run %s", change.State, run.Status)
			}
			if !c.ends {
				if change.ClosedAs != "" {
					t.Fatalf("an open change records closed as %q", change.ClosedAs)
				}
				return
			}
			// The close is recorded as what it was, and every read of the
			// change carries it: the change, the project's list, the history.
			if change.ClosedAs != workflow.ClosedNothingToChange {
				t.Fatalf("the change records closed as %q", change.ClosedAs)
			}
			list, err := a.work.Changes(a.ctx, change.ProjectID, workflow.ChangeList{})
			if err != nil || len(list) != 1 || list[0].ClosedAs != workflow.ClosedNothingToChange {
				t.Fatalf("the change list: %+v %v", list, err)
			}
			history, err := a.work.ChangeHistory(a.ctx, workflow.HistoryQuery{ProjectID: change.ProjectID})
			if err != nil || len(history) != 1 || history[0].ClosedAs != workflow.ClosedNothingToChange {
				t.Fatalf("the change history: %+v %v", history, err)
			}
			closed := runIDEvents(t, a, runID, "run.abandoned")
			if len(closed) == 0 || !strings.HasPrefix(fmt.Sprint(closed[0]["reason"]), "Nothing to change: the build step found nothing to change") || !strings.Contains(fmt.Sprint(closed[0]["reason"]), "no dependencies") || closed[0]["by"] != "apptown-system" {
				t.Fatalf("closing receipts: %v", closed)
			}
		})
	}
}

// pendingStepNamed is the run's pending step of that name, or the zero id.
func pendingStepNamed(t *testing.T, a *taskAppTown, runID ids.ID, name string) ids.ID {
	t.Helper()
	snapshot, err := a.work.Snapshot(a.ctx, runID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snapshot.Steps {
		if step.Name == name && step.Status == workflow.StatusPending {
			return step.ID
		}
	}
	return ids.Nil
}

// openAdoptionChange opens an adoption Change on the fixture's project as
// the adoption door does: Adoption prepares the candidate (here, the
// project's own selection again) and records it as the Change's target in
// Work's admission.
func openAdoptionChange(f *taskAppTown) (*workflow.Change, error) {
	directory := identity.New(f.db, identity.Config{Projects: project.NewCatalogue(f.db)})
	adoptions := adoption.New(f.db, adoption.Dependencies{Ways: agentCatalog{}, Authority: access.Checked{Authority: directory, Projects: project.New(f.db, directory)}})
	// The adoption door's caller, as a request carries it: the actor the
	// receipt names is the verified caller's.
	ctx := access.WithCaller(f.ctx, access.Caller{Principal: access.Principal{Kind: "user", Name: taskAppTownAdministrator, Via: "session"}, Verified: true, Receipt: taskAppTownAdministrator})
	c, err := adoptions.Prepare(ctx, f.projectID, adoption.Request{})
	if err != nil {
		return nil, err
	}
	return f.work.Uplift(ctx, workflow.Uplift{ProjectID: f.projectID, ExpectedRevision: c.ExpectedRevision, Acceptance: workflow.Acceptance{TargetDigest: c.Digest},
		Words: []string{"the fixture's own"}, Target: adoptionTarget{adoptions, *c}})
}

// adoptionTarget records an adoption Change's target with Adoption, as the
// composition root's adapter does (workcommands).
type adoptionTarget struct {
	adoptions *adoption.Service
	candidate adoption.Candidate
}

func (t adoptionTarget) RecordTargetIn(ctx context.Context, tx *database.Tx, in workflow.TargetChange) (workflow.RecordedTarget, error) {
	got, err := t.adoptions.RecordTargetIn(ctx, tx, adoption.Target{ProjectID: in.ProjectID, ChangeID: in.ChangeID, Candidate: t.candidate, Actor: in.Actor})
	return workflow.RecordedTarget{ID: got.ID, Candidate: got.Candidate}, err
}

// An agent step admitted with executor: local (a definition saved before
// project steps ran only in containers) is refused before Pi runs, in words
// that say to remove it. The workflow default is refused the same way
// (TestAnAgentStepTheWorkerCannotRunIsRefusedBeforeAnyEffect).
func TestAnAdmittedLocalAgentRunsNowhere(t *testing.T) {
	t.Parallel()
	f, _, stepID := admittedAgentStep(t, `{"prompt":"do it","executor":"local"}`)
	pi := installFakePi(f, func(int, execution.Request) execution.Outcome {
		return execution.Outcome{Output: piSays("must not run")}
	})
	if err := f.work.ExecuteStep(f.ctx, stepID); err != nil {
		t.Fatal(err)
	}
	status, ev := stepStatus(t, f, stepID)
	if status != workflow.StatusError || !strings.Contains(fmt.Sprint(ev["reason"]), "executor: local is not supported") {
		t.Fatalf("the step: %s %v", status, ev)
	}
	pi.mu.Lock()
	defer pi.mu.Unlock()
	if len(pi.calls) != 0 {
		t.Fatal("Pi ran for a refused step")
	}
}
