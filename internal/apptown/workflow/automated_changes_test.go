package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type automatedRetentionFake struct{ held bool }

func (f *automatedRetentionFake) RetainsWorkIn(context.Context, database.Queryer, ids.ID, []ids.ID) (bool, error) {
	return f.held, nil
}

type runningImagesFake struct{ rows []RunningImages }

func (f *runningImagesFake) RunningImages(context.Context, ids.ID) ([]RunningImages, error) {
	return f.rows, nil
}

func automatedFixture(t *testing.T) (*Service, *Change, string, *automatedRetentionFake, *runningImagesFake) {
	t.Helper()
	db := testDB(t)
	pid := workflowTestProject(t, db, "scan-project", "Scan project", "main")
	data := t.TempDir()
	repo, err := (project.RepositoryRef{ID: pid, Slug: "scan-project"}).Path(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repo, "init", "-q", "-b", "main")
	gittest.Run(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.org", "commit", "--allow-empty", "-qm", "main")
	ctx := access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule"}})
	created, err := database.Transact(ctx, db, func(tx *database.Tx) (CreatedChange, error) {
		return CreateChangeIn(ctx, tx, ChangeDraft{ProjectID: pid, Title: "Scan project", Queue: "security"}, "apptown-system", time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	ch := created.Change
	gittest.Run(t, repo, "branch", ch.Branch)
	retention, running := &automatedRetentionFake{}, &runningImagesFake{}
	s := New(context.Background(), db, Dependencies{DataDir: data, Repositories: &scm.RepositoryLocks{}, Abandonment: abandonRecorderFunc(noAbandonEffects), Retention: retention, RunningImages: running})
	s.authority = allowProject(pid)
	t.Cleanup(s.Stop)
	return s, ch, repo, retention, running
}

func TestQueueChangesHaveAnAccessibleRecordOutsideTheChangeList(t *testing.T) {
	s, ch, _, _, _ := automatedFixture(t)
	list, err := s.Changes(t.Context(), ch.ProjectID, ChangeList{})
	if err != nil || len(list) != 0 {
		t.Fatalf("queue in Change list: %+v %v", list, err)
	}
	attempts, err := s.Attempts(t.Context(), ch.ProjectID, "security", 20)
	if err != nil || len(attempts) != 1 || attempts[0].OpenChange != ch.ID {
		t.Fatalf("unreachable queue Change: %+v %v", attempts, err)
	}
	if _, err := s.Change(t.Context(), attempts[0].OpenChange); err != nil {
		t.Fatal(err)
	}
	// An authorized person's edit promotes the Change irreversibly.
	ctx := access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{Kind: "user", Name: "verified-person", Via: "session"}})
	if err := s.MayAbandon(ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
	stillHidden, err := s.Changes(ctx, ch.ProjectID, ChangeList{})
	if err != nil || len(stillHidden) != 0 {
		t.Fatalf("a permission read promoted automated work: %+v %v", stillHidden, err)
	}
	title := "Look into the findings"
	if _, err := s.Edit(ctx, ChangeEdit{ID: ch.ID, Title: &title, ExpectedRevision: revision(0)}); err != nil {
		t.Fatal(err)
	}
	list, err = s.Changes(t.Context(), ch.ProjectID, ChangeList{})
	if err != nil || len(list) != 1 {
		t.Fatalf("touched work still hidden: %+v %v", list, err)
	}
	if _, err := s.Abandon(t.Context(), StopWork{ChangeID: ch.ID, Reason: "finished looking"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeAutomatedChanges(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Change(t.Context(), ch.ID); err != nil {
		t.Fatalf("touched work purged: %v", err)
	}
}

func TestAutomatedPurgeKeepsExactScanSubjectsAfterRestart(t *testing.T) {
	s, ch, repo, retention, running := automatedFixture(t)
	run := admitAutomatedRun(t, s, ch)
	artifact, err := AddArtifactIn(t.Context(), s.db, run.ID, "report", "scan", "", "disposable execution report", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	main := gittest.Run(t, repo, "rev-parse", "HEAD")
	dep, bundle := idstest.N(10), idstest.N(11)
	running.rows = []RunningImages{{DeploymentID: dep, BundleID: bundle, Images: map[string]string{"web": "registry.example/p@sha256:a"}}, {DeploymentID: idstest.N(12), Images: map[string]string{}}}
	now := time.Now().UTC()
	observations := []conformance.ScanObservation{
		{Subject: conformance.ScanSubject{On: "source", Commit: main}, Scanner: "osv", Verdict: "pass"},
		{Subject: conformance.ScanSubject{On: "running", DeploymentID: dep, BundleID: bundle, Part: "web", Digest: "registry.example/p@sha256:a"}, Scanner: "trivy", Verdict: "fail", Findings: []conformance.Finding{{Finding: check.Finding{Identifiers: []string{"CVE-OS"}, Package: &check.PackageRef{Name: "libssl", Ecosystem: "Debian"}}}}},
		{Subject: conformance.ScanSubject{On: "running", DeploymentID: idstest.N(12)}, Verdict: "error", Reason: "the running bundle is not known"},
	}
	// These are scan-owner operations, independent of disposable execution rows.
	// All three answers share the run that collected this observing scan.
	for i := range observations {
		o := &observations[i]
		o.ID = ids.New(now)
		o.ProjectID = ch.ProjectID
		o.ChangeID = ch.ID
		o.RunID = run.ID
		o.StepID = idstest.N(uint64(21 + i))
		o.Attempt = 1
		o.ObservedAt = now
		o.BindingVersion = 7
		o.ConfigHash = "exact-config-hash"
		o.Configuration = `{"image":"scanner@sha256:abc","command":["scan"]}`
		o.StepConfig = json.RawMessage(`{"baseline":"none"}`)
		if err := s.db.InTx(t.Context(), func(tx *database.Tx) error { return conformance.RecordScanObservationIn(t.Context(), tx, *o) }); err != nil {
			t.Fatal(err)
		}
	}
	ws := (Layout{DataDir: s.dataDir}).ChangeWorkspace(ch.ID)
	transcript := filepath.Join(ws, ".apptown", "sessions", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("disposable transcript"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Abandon(t.Context(), StopWork{ChangeID: ch.ID, Reason: "observation finished"}); err != nil {
		t.Fatal(err)
	}
	retention.held = true
	if err := s.PurgeAutomatedChanges(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Change(t.Context(), ch.ID); err != nil {
		t.Fatalf("purged while retained: %v", err)
	}
	retention.held = false
	if err := s.PurgeAutomatedChanges(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Change(t.Context(), ch.ID); err == nil {
		t.Fatal("execution record survived purge")
	}
	history, err := s.ChangeHistory(t.Context(), HistoryQuery{ProjectID: ch.ProjectID})
	if err != nil || len(history) != 0 {
		t.Fatalf("queue Change leaked into ordinary history: %+v %v", history, err)
	}

	if _, err := s.Run(t.Context(), run.ID); err == nil {
		t.Fatal("disposable run survived purge")
	}
	if _, err := s.Artifact(t.Context(), artifact); err == nil {
		t.Fatal("disposable report survived purge")
	}
	if _, err := os.Stat(ws); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace or transcript remains: %v", err)
	}
	if branch := gittest.Run(t, repo, "branch", "--list", ch.Branch); branch != "" {
		t.Fatalf("branch remains: %s", branch)
	}
	// Reconstruct the owner from persistent state, as a process restart does.
	restarted := New(context.Background(), s.db, Dependencies{DataDir: s.dataDir, RunningImages: running})
	restarted.authority = allowProject(ch.ProjectID)
	defer restarted.Stop()
	scans, err := restarted.LatestMainScans(t.Context(), ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if scans.Known || len(scans.Source) != 1 || len(scans.Running) != 2 {
		t.Fatalf("lost one of clean, finding, unknown: %+v", scans)
	}
	if scans.Source[0].Subject.Commit != main || scans.Source[0].BindingVersion != 7 || scans.Source[0].ConfigHash != "exact-config-hash" || scans.Source[0].Configuration != observations[0].Configuration {
		t.Fatalf("lost scanner configuration: %+v", scans.Source[0])
	}
	attempts, err := restarted.Attempts(t.Context(), ch.ProjectID, "security", 20)
	if err != nil || len(attempts) != 1 || len(attempts[0].Observations) != 3 || !attempts[0].OpenChange.IsZero() {
		t.Fatalf("lost one-line record: %+v %v", attempts, err)
	}
	gittest.Run(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.org", "commit", "--allow-empty", "-qm", "unrelated landing")
	running.rows[0].Images["web"] = "registry.example/p@sha256:b"
	scans, err = restarted.LatestMainScans(t.Context(), ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if scans.Known || len(scans.Source) != 0 || len(scans.Historical) < 2 {
		t.Fatalf("old software became current: %+v", scans)
	}
}

func admitAutomatedRun(t *testing.T, s *Service, ch *Change, kinds ...string) *Run {
	t.Helper()
	if len(kinds) == 0 {
		kinds = []string{"scan"}
	}
	var steps []schema.Step
	for i, kind := range kinds {
		steps = append(steps, schema.Step{Name: fmt.Sprintf("observe-%d", i), Uses: kind})
	}
	adopted, err := adoption.CurrentReceiptIDIn(t.Context(), s.db, ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.runAdmissions.records.create(t.Context(), StartRequest{ProjectID: ch.ProjectID, ChangeID: ch.ID, WorkflowName: "scan", InputsJSON: "{}", requestedBy: "apptown-system"}, PreparedRun{AdoptionID: adopted, Workflow: DefinitionRef{Name: "scan", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "scan", Steps: steps}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.runAdmissions.records.prepared(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestALandingPromotesAutomatedWorkAndKeepsItsExecutionMaterial(t *testing.T) {
	s, ch, _, _, _ := automatedFixture(t)
	run := admitAutomatedRun(t, s, ch)
	artifact, err := AddArtifactIn(t.Context(), s.db, run.ID, "report", "scan", "", "execution report", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.InTx(t.Context(), func(tx *database.Tx) error {
		_, err := TransitionChangeIn(t.Context(), tx, ch.ProjectID, ch.ID, ChangeMove{Action: LandChange, RunID: run.ID}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Abandon(t.Context(), StopWork{ChangeID: ch.ID, Reason: "landing stopped"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeAutomatedChanges(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, err := s.Change(t.Context(), ch.ID)
	if err != nil || !current.AutomatedOrigin {
		t.Fatalf("lost promoted origin: %+v %v", current, err)
	}
	if _, err := s.Artifact(t.Context(), artifact); err != nil {
		t.Fatalf("lost promoted execution material: %v", err)
	}
}

type taskRetention struct{}

func (taskRetention) RetainsWorkIn(ctx context.Context, db database.Queryer, change ids.ID, runs []ids.ID) (bool, error) {
	return tasks.RetainsWorkIn(ctx, db, change, runs)
}

func TestAutomatedPurgeWaitsForARunningTaskAndItsRelease(t *testing.T) {
	s, ch, _, _, _ := automatedFixture(t)
	run := admitAutomatedRun(t, s, ch)
	steps, err := s.RunSteps(t.Context(), run.ID)
	if err != nil || len(steps) != 1 {
		t.Fatalf("steps: %+v %v", steps, err)
	}
	records := tasks.New(s.db)
	admission := tasks.Admission{Operation: "scan-purge-test", Invocation: tasks.ScriptInvocation, Owner: tasks.Owner{ProjectID: ch.ProjectID, RunID: run.ID, StepID: steps[0].ID, Attempt: 1}, Backend: "docker", Container: "scan-purge-test", Digest: "test-digest", Admitted: execution.Admission{Profile: "docker", Network: execution.NetworkNone, Limits: execution.Limits{CPUs: 1, WallClock: time.Minute}, Expires: time.Now().Add(time.Hour)}, Deadline: time.Now().Add(time.Minute)}
	if _, err := records.Reserve(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	if err := records.RecordProvider(t.Context(), admission.Operation, "task-provider-id"); err != nil {
		t.Fatal(err)
	}
	if err := records.Start(t.Context(), admission.Operation); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Abandon(t.Context(), StopWork{ChangeID: ch.ID, Reason: "scan stopped"}); err != nil {
		t.Fatal(err)
	}
	s.retention = taskRetention{}
	if err := s.PurgeAutomatedChanges(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Change(t.Context(), ch.ID); err != nil {
		t.Fatalf("purged a running task: %v", err)
	}
	code := 0
	if err := records.Settle(t.Context(), admission.Operation, &code, "stopped"); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeAutomatedChanges(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Change(t.Context(), ch.ID); err != nil {
		t.Fatalf("purged an unreleased task: %v", err)
	}
	if err := records.ConfirmRelease(t.Context(), admission.Operation); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeAutomatedChanges(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Change(t.Context(), ch.ID); err == nil {
		t.Fatal("released task still retains the untouched Change")
	}
}
