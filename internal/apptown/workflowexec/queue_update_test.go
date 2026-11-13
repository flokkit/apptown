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
	"github.com/flokkit/apptown/internal/apptown/attention"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
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
	run, err := f.work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: name})
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
	testQueueProposals(t, true)
}

func TestThreeUninterruptedFailuresStopOnlyTheirQueue(t *testing.T) {
	testQueueProposals(t, false)
}

func testQueueProposals(t *testing.T, skip bool) {
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "update", OnEnter: []schema.Step{{Name: "update", Uses: FixKind, With: map[string]any{"kind": "update"}}}, Steps: []schema.Step{{Name: "tests", Uses: TestsKind}}}}}
	f, _ := newNodeFixFixture(t, "updates", spec)
	// Attention lists the queues the project's adopted workflows declare: pin
	// the stock update workflow, whose schedule declares queue "updates".
	f.ways.SyncStockWorkflows(t.Context())
	version, _, err := f.ways.WorkflowVersion(t.Context(), "janitor-update", 0)
	if err != nil {
		t.Fatal(err)
	}
	adopted, err := adoption.CurrentReceiptIn(t.Context(), f.db, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	receipt := adopted.Receipt
	receipt.Workflows = append(receipt.Workflows, adoption.ReceiptWorkflow{Name: "janitor-update", Version: version, Origin: "stock"})
	if err := f.db.InTx(t.Context(), func(tx *database.Tx) error {
		return adoption.RecordIn(f.ctx, tx, f.projectID, adopted.Revision+1, adoption.AdoptionEdit, "adopt the update workflow", receipt, taskAppTownAdministrator, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	// Weekly updates move both packages; the targeted security path leaves the
	// unrelated package alone and therefore passes this same real test command.
	contract := `app:
  web:
    build: .
    public: true
    tools: [node]
    test:
      - "node -e \"if(require('./package-lock.json').packages['node_modules/untargeted'].version !== '1.0.0') { console.error('untargeted update needs a code change'); process.exit(1) }\""
`
	if err := os.WriteFile(filepath.Join(f.hub, ".apptown/app.yaml"), []byte(contract), 0644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, f.hub, "add", ".")
	gittest.Run(t, f.hub, "-c", "user.name=fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "update checks")
	first := queueRun(t, f, "updates", "updates")
	steps := queueSteps(t, f, first)
	queueExecute(t, f, steps["update"])
	queueExecute(t, f, steps["tests"])
	state, err := f.work.QueueState(f.ctx, f.projectID, "updates")
	if err != nil || state.Failed != 1 || state.State != "stuck" {
		t.Fatalf("first failure %+v %v", state, err)
	}
	quiet := func() {
		f.definitions["quiet"] = schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "scan", Steps: []schema.Step{{Name: "scan", Uses: "scan"}, {Name: "running", Uses: "scan"}}}}}
		f.scan = func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			if task.Step().Name == "running" {
				if err := task.InAttempt("scan", func(tx *database.Tx, attempt int64) error {
					return conformance.RecordScanObservationIn(task.Context(), tx, conformance.ScanObservation{ProjectID: f.projectID, RunID: task.Run().ID, StepID: task.Step().ID, Attempt: attempt, Subject: conformance.ScanSubject{On: "running"}, ObservedAt: time.Now(), Verdict: workflow.StatusNotApplicable.String(), Reason: "no deployments"})
				}); err != nil {
					t.Error(err)
					return
				}
				task.Finish(workflow.EncodeOutcome(workflow.StatusNotApplicable, map[string]string{"reason": "no deployments"}, nil))
				return
			}
			main := gittest.Run(t, f.hub, "rev-parse", "main")
			if err := task.InAttempt("scan", func(tx *database.Tx, attempt int64) error {
				return conformance.RecordScanObservationIn(task.Context(), tx, conformance.ScanObservation{ProjectID: f.projectID, RunID: task.Run().ID, StepID: task.Step().ID, Attempt: attempt, Subject: conformance.ScanSubject{On: "source", Commit: strings.TrimSpace(main)}, Scanner: "fixture", ExpectedScanners: []string{"fixture"}, ObservedAt: time.Now(), Verdict: "pass"})
			}); err != nil {
				t.Error(err)
				return
			}
			task.Finish(workflow.EncodeOutcome(workflow.StatusPass, map[string]string{"reason": "no findings"}, nil))
		}
		run := queueRun(t, f, "quiet", "updates")
		queueExecute(t, f, queueSteps(t, f, run)["scan"])
		queueExecute(t, f, queueSteps(t, f, run)["running"])
		if _, err := f.work.Advance(queueCaller(f), run.ID); err != nil {
			t.Fatal(err)
		}
	}
	quiet()
	duplicate := queueRun(t, f, "updates", "updates")
	steps = queueSteps(t, f, duplicate)
	queueExecute(t, f, steps["update"])
	got := queueSteps(t, f, duplicate)
	if got["update"].Status != workflow.StatusSkipped || got["tests"].Status != workflow.StatusPending {
		t.Fatalf("same proposal reached tests: %+v", got)
	}
	if err := f.work.SkipUpdate(f.ctx, f.projectID, "updates", duplicate.ChangeID); err == nil {
		t.Fatal("a stale page skipped a different update")
	}
	remaining := 2
	if skip {
		if err := f.work.SkipUpdate(f.ctx, f.projectID, "updates", state.ChangeID); err != nil {
			t.Fatal(err)
		}
		skipped := queueRun(t, f, "updates", "updates")
		steps = queueSteps(t, f, skipped)
		queueExecute(t, f, steps["update"])
		got = queueSteps(t, f, skipped)
		if got["update"].Status != workflow.StatusSkipped || got["tests"].Status != workflow.StatusPending {
			t.Fatalf("skipped proposal reached tests: %+v", got)
		}
		state, err = f.work.QueueState(f.ctx, f.projectID, "updates")
		if err != nil || state.Failed != 0 || state.Stopped() {
			t.Fatalf("skip did not end the episode: %+v %v", state, err)
		}
		remaining = 3
	}
	for n := 1; n <= remaining; n++ {
		if err := os.WriteFile(filepath.Join(f.hub, "progress.txt"), []byte(fmt.Sprintf("main %d", n)), 0644); err != nil {
			t.Fatal(err)
		}
		gittest.Run(t, f.hub, "add", ".")
		gittest.Run(t, f.hub, "-c", "user.name=fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", fmt.Sprintf("main %d", n))
		run := queueRun(t, f, "updates", "updates")
		steps = queueSteps(t, f, run)
		queueExecute(t, f, steps["update"])
		queueExecute(t, f, steps["tests"])
	}
	quiet()
	state, err = f.work.QueueState(f.ctx, f.projectID, "updates")
	if err != nil || state.State != "stopped" || state.Failed != 3 || len(state.Packages) != 2 || !strings.Contains(state.Reason, "tests") {
		t.Fatalf("stopped state %+v %v", state, err)
	}
	attempts, err := f.work.Attempts(f.ctx, f.projectID, "updates", 100)
	if err != nil {
		t.Fatal(err)
	}
	open := 0
	skips := 0
	for _, a := range attempts {
		if !a.OpenChange.IsZero() {
			open++
		}
		if a.Outcome == "skipped" {
			skips++
		}
		if a.Schedule == "" || !a.RuleID.IsZero() || a.RuleRevision != 0 {
			t.Fatalf("fabricated or missing origin: %+v", a)
		}
	}
	wantSkips := 0
	if skip {
		wantSkips = 1
	}
	if open != 1 || skips != wantSkips {
		t.Fatalf("open=%d skips=%d history=%+v", open, skips, attempts)
	}
	directory := identity.New(f.db, identity.Config{Projects: project.NewCatalogue(f.db)})
	att, err := (attention.Scans{Work: f.work, Projects: project.New(f.db, directory)}).ProjectSnapshot(f.ctx, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(att.Items) != 1 || !strings.Contains(att.Items[0].Title, "stopped after 3") || !strings.Contains(att.Items[0].Detail, "untargeted") {
		t.Fatalf("attention %+v", att.Items)
	}
	// A distinct security queue can still make a targeted update. Its normal
	// test command passes because the unrelated package remains unchanged.
	raw, err := stock.FS.ReadFile("janitor-scan.yaml")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := schema.ParseWorkflow(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = stock.FS.ReadFile("janitor-fix.yaml")
	if err != nil {
		t.Fatal(err)
	}
	childDef, err := schema.ParseWorkflow(raw)
	if err != nil {
		t.Fatal(err)
	}
	f.definitions["janitor-scan"], f.definitions["janitor-fix"] = parent.Spec, childDef.Spec
	// The external boot and scanner adapters answer through real step claims.
	f.preview = func(task *workflow.Execution) {
		if _, err := task.Claim(); err != nil {
			t.Error(err)
			return
		}
		task.Finish(workflow.EncodeOutcome(workflow.StatusPass, map[string]string{"reason": "fixture boot answered"}, nil))
	}
	findingName := "example"
	findingSeverity := "high"
	f.scan = func(task *workflow.Execution) {
		if _, err := task.Claim(); err != nil {
			t.Error(err)
			return
		}
		status := workflow.StatusPass
		var findings []check.Finding
		if task.Run().WorkflowName == "janitor-scan" && strings.HasPrefix(task.Step().Name, "scan-source") {
			status = workflow.StatusFail
			findings = []check.Finding{{Fingerprint: "security-" + findingName, Kind: "vuln", Severity: findingSeverity, Package: &check.PackageRef{Name: findingName, Ecosystem: "npm", Version: "1.0.0", FixedIn: "1.0.1"}}}
			if findingSeverity == "low" {
				findings = append(findings, check.Finding{Fingerprint: "os-high", Kind: "vuln", Severity: "high", Package: &check.PackageRef{Name: "kernel", Ecosystem: "Debian"}})
			}
		}
		if len(findings) > 0 {
			if err := task.InAttempt("scan", func(tx *database.Tx, attempt int64) error {
				return conformance.RecordScanObservationIn(task.Context(), tx, conformance.ScanObservation{ProjectID: f.projectID, RunID: task.Run().ID, StepID: task.Step().ID, Attempt: attempt, Subject: conformance.ScanSubject{On: "source"}, Scanner: "fixture", ObservedAt: time.Now(), Verdict: "fail", Findings: conformance.WrapFindings(findings)})
			}); err != nil {
				t.Error(err)
				return
			}
		}
		verdict := workflow.VerdictPass
		if status == workflow.StatusFail {
			verdict = workflow.VerdictFail
		}
		task.Finish((workflow.CheckEvidence{Checks: []workflow.CheckReceipt{{Check: task.Step().Name, Evaluator: "fixture-scanner", Verdict: verdict}}}).Outcome(status, map[string]any{"reason": "fixture scanner answered"}))
	}
	securityRun := queueRun(t, f, "janitor-scan", "security")
	steps = queueSteps(t, f, securityRun)
	for name, step := range steps {
		if strings.HasPrefix(name, "scan-") {
			queueExecute(t, f, step)
		}
	}
	queueExecute(t, f, steps["fix"])
	steps = queueSteps(t, f, securityRun)
	var called struct {
		ChildRunID ids.ID `json:"childRunId"`
	}
	if err := json.Unmarshal(steps["fix"].Evidence, &called); err != nil || called.ChildRunID.IsZero() {
		t.Fatalf("security call %s: %v", steps["fix"].Evidence, err)
	}
	child, err := f.work.Run(f.ctx, called.ChildRunID)
	if err != nil {
		t.Fatal(err)
	}
	childSteps := queueSteps(t, f, child)
	for _, name := range []string{"update", "tests", "boot", "scan-source", "scan-image", "routine"} {
		queueExecute(t, f, childSteps[name])
		status, evidence := stepStatus(t, f, childSteps[name].ID)
		if status != workflow.StatusPass {
			t.Fatalf("security %s %s: %+v", name, status, evidence)
		}
	}
	if _, err := f.work.Advance(queueCaller(f), child.ID); err != nil {
		t.Fatal(err)
	}
	steps = queueSteps(t, f, securityRun)
	if steps["accept"].Status != workflow.StatusPending {
		t.Fatalf("acceptance did not wait for a person: %+v", steps["accept"])
	}
	directory = identity.New(f.db, identity.Config{Projects: project.NewCatalogue(f.db)})
	if _, err := directory.Grant(f.ctx, identity.Spec{SubjectType: "user", Subject: taskAppTownAdministrator, Role: access.RoleProjectApprover, Scope: access.ScopeProject, ScopeID: f.projectID.String(), By: taskAppTownAdministrator}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.work.Approve(f.ctx, workflow.Approval{StepID: steps["accept"].ID, Decision: workflow.VoteApprove, Comment: "Accept the targeted fix"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.work.Advance(f.ctx, securityRun.ID); err != nil {
		t.Fatal(err)
	}
	f.definitions["land"] = schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{{Name: "tests", Uses: TestsKind}}}}}
	landing, err := f.work.RequestLanding(f.ctx, securityRun.ChangeID)
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatalf("request landing: %+v %v", landing, err)
	}
	if err := f.work.ExecuteStep(f.ctx, queueSteps(t, f, landing)["tests"].ID); err != nil {
		t.Fatal(err)
	}
	result, err := f.work.Advance(f.ctx, landing.ID)
	if err != nil || result == nil || !result.Advanced {
		t.Fatalf("person-led landing %+v %v", result, err)
	}
	landed, err := f.work.Change(f.ctx, securityRun.ChangeID)
	if err != nil || landed.State != workflow.ChangeShipped {
		t.Fatalf("security Change did not land: %+v %v", landed, err)
	}
	state, err = f.work.QueueState(f.ctx, f.projectID, "updates")
	if err != nil || state.State != "stopped" || state.Failed != 3 {
		t.Fatalf("security landing reset weekly failures: %+v %v", state, err)
	}
	security, err := f.work.QueueState(f.ctx, f.projectID, "security")
	if err != nil || security.State == "stopped" || security.Failed != 0 {
		t.Fatalf("other queue stopped: %+v %v", security, err)
	}

	// A failed targeted attempt is skipped by a person. Its targets, retained
	// separately from the Change, must suppress the next identical finding.
	findingName = "untargeted"
	f.definitions["janitor-fix"] = schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "fix", Steps: []schema.Step{{Name: "update", Uses: FixKind, With: map[string]any{"kind": "update", "for": "findings"}}, {Name: "tests", Uses: TestsKind}}}}}
	failed := queueRun(t, f, "janitor-scan", "security")
	fs := queueSteps(t, f, failed)
	for name, step := range fs {
		if strings.HasPrefix(name, "scan-") {
			queueExecute(t, f, step)
		}
	}
	queueExecute(t, f, fs["fix"])
	fs = queueSteps(t, f, failed)
	if err := json.Unmarshal(fs["fix"].Evidence, &called); err != nil {
		t.Fatal(err)
	}
	failedChild, err := f.work.Run(f.ctx, called.ChildRunID)
	if err != nil {
		t.Fatal(err)
	}
	fcs := queueSteps(t, f, failedChild)
	queueExecute(t, f, fcs["update"])
	queueExecute(t, f, fcs["tests"])
	if state, err := f.work.QueueState(f.ctx, f.projectID, "security"); err != nil || state.State != "stuck" {
		t.Fatalf("failed security %+v %v", state, err)
	}
	if err := f.work.SkipUpdate(f.ctx, f.projectID, "security", failed.ChangeID); err != nil {
		t.Fatal(err)
	}
	again := queueRun(t, f, "janitor-scan", "security")
	as := queueSteps(t, f, again)
	for name, step := range as {
		if strings.HasPrefix(name, "scan-") {
			queueExecute(t, f, step)
		}
	}
	queueExecute(t, f, as["fix"])
	as = queueSteps(t, f, again)
	var notNeeded struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(as["fix"].Evidence, &notNeeded); err != nil || as["fix"].Status != workflow.StatusNotApplicable || !strings.Contains(notNeeded.Reason, "nothing new since") || as["scan-source"].Status != workflow.StatusFail {
		t.Fatalf("skipped target called again: %s %v", as["fix"].Evidence, err)
	}

	f.definitions["security-failure"] = schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "attempt", Uses: ScriptKind, With: map[string]any{"run": "false"}}}}}}
	f.scriptRunner = func(task *workflow.Execution) {
		if _, err := task.Claim(); err != nil {
			t.Error(err)
			return
		}
		if err := task.StartFix(nil); err != nil {
			t.Error(err)
			return
		}
		task.Finish(workflow.EncodeOutcome(workflow.StatusFail, map[string]any{"reason": "fixture command failed"}, nil))
	}
	for n := 0; n < 3; n++ {
		run := queueRun(t, f, "security-failure", "security")
		queueExecute(t, f, queueSteps(t, f, run)["attempt"])
	}
	threshold := f.definitions["janitor-scan"]
	threshold.Stages[0].Steps[0].With["failOn"] = "high"
	f.definitions["janitor-scan"] = threshold
	for _, severity := range []string{"low", "high"} {
		findingName = "new-" + severity
		findingSeverity = severity
		run := queueRun(t, f, "janitor-scan", "security")
		steps := queueSteps(t, f, run)
		for name, step := range steps {
			if strings.HasPrefix(name, "scan-") {
				queueExecute(t, f, step)
			}
		}
		if state, err := f.work.QueueState(f.ctx, f.projectID, "security"); err != nil || state.State != "stopped" {
			t.Fatalf("discovery cleared the stopped queue: %+v %v", state, err)
		}
		queueExecute(t, f, steps["fix"])
		steps = queueSteps(t, f, run)
		var result struct {
			ChildRunID ids.ID `json:"childRunId"`
		}
		if err := json.Unmarshal(steps["fix"].Evidence, &result); err != nil {
			t.Fatal(err)
		}
		if severity == "low" && (steps["fix"].Status != workflow.StatusNotApplicable || !strings.Contains(string(steps["fix"].Evidence), "reason")) || severity == "high" && result.ChildRunID.IsZero() {
			t.Fatalf("stopped severity %s: %s", severity, steps["fix"].Evidence)
		}
	}
}

// Scans have no running-image subjects in the command fixture.
type fixtureRunningImages struct{}

func (fixtureRunningImages) RunningImages(context.Context, ids.ID) ([]workflow.RunningImages, error) {
	return nil, nil
}

func TestProposalFingerprintBindsMainListingTargetsAndAdoption(t *testing.T) {
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "propose", Uses: ScriptKind, With: map[string]any{"run": "true"}}}}}}
	f, _ := newNodeFixFixture(t, "fingerprint", spec)
	main, listing := "main-a", json.RawMessage(`[{"package":"example","version":"1.0.1"}]`)
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
		same, _, err = task.KeepProposal(main, listing, nil)
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
	listing = json.RawMessage(`[{"package":"example","version":"1.0.2"}]`)
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
