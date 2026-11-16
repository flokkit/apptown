package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/complaint"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/identity"
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
	s := New(context.Background(), db, Dependencies{DataDir: data, Repositories: &scm.RepositoryLocks{}, Abandonment: abandonRecorderFunc(noAbandonEffects), Retention: retention, ScanSchedules: scheduledScans{}, RunningImages: running})
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
		{Subject: conformance.ScanSubject{On: "source", Commit: main, Tree: gittest.Run(t, repo, "rev-parse", main+"^{tree}")}, Scanner: "osv", Verdict: "pass"},
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
	restarted := New(context.Background(), s.db, Dependencies{DataDir: s.dataDir, ScanSchedules: scheduledScans{}, RunningImages: running})
	restarted.authority = allowProject(ch.ProjectID)
	defer restarted.Stop()
	scans, err := restarted.LatestMainScans(t.Context(), ch.ProjectID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if scans.Known || len(scans.Source) != 1 || len(scans.Running) != 1 || len(scans.Historical) != 1 {
		t.Fatalf("lost one of clean, finding, unknown: %+v", scans)
	}
	if scans.Source[0].Subject.Commit != main || scans.Source[0].BindingVersion != 7 || scans.Source[0].ConfigHash != "exact-config-hash" || scans.Source[0].Configuration != observations[0].Configuration {
		t.Fatalf("lost scanner configuration: %+v", scans.Source[0])
	}
	attempts, err := restarted.Attempts(t.Context(), ch.ProjectID, "security", 20)
	if err != nil || len(attempts) != 1 || len(attempts[0].Observations) != 3 || !attempts[0].OpenChange.IsZero() {
		t.Fatalf("lost one-line record: %+v %v", attempts, err)
	}
	if err := os.WriteFile(filepath.Join(repo, "changed.txt"), []byte("different tree"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repo, "add", "changed.txt")
	gittest.Run(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.org", "commit", "-qm", "unrelated landing")
	running.rows[0].Images["web"] = "registry.example/p@sha256:b"
	scans, err = restarted.LatestMainScans(t.Context(), ch.ProjectID, 10)
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
	if ch.Type == "uplift" {
		adopted = ch.TargetAdoptionID
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

type recordRetention struct{}

func (recordRetention) RetainsWorkIn(ctx context.Context, db database.Queryer, change ids.ID, runs []ids.ID) (bool, error) {
	held, err := adrs.RetainsWorkIn(ctx, db, change, runs)
	if held || err != nil {
		return held, err
	}
	return complaint.RetainsWorkIn(ctx, db, change, runs)
}

func TestAutomatedPurgeKeepsWorkNamedByDecisionsAndComplaints(t *testing.T) {
	for _, kind := range []string{"decision", "complaint change", "complaint run", "unreferenced"} {
		t.Run(kind, func(t *testing.T) {
			s, ch, _, _, _ := automatedFixture(t)
			run := admitAutomatedRun(t, s, ch)
			ctx := access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule", ProjectID: ch.ProjectID}})
			accounts := identity.New(s.db, identity.Config{Projects: project.NewCatalogue(s.db)})
			if err := accounts.SeedAdministrator(t.Context(), "retention-reader"); err != nil {
				t.Fatal(err)
			}
			person := access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{Kind: "user", Name: "retention-reader", Via: "session"}})
			records := adrs.New(s.db, access.Checked{Authority: accounts, Projects: project.New(s.db, accounts)}, nil, nil)
			complaints := complaint.NewBox(s.db, nil, nil)
			var recordID ids.ID
			switch kind {
			case "decision":
				r, err := records.Create(person, adrs.Create{ProjectID: ch.ProjectID, Title: "Keep the decision's context", ChangeID: ch.ID})
				if err != nil {
					t.Fatal(err)
				}
				recordID = r.ID
			case "complaint change", "complaint run":
				e := complaint.Entry{ProjectID: ch.ProjectID, Text: "Keep this complaint's context", Actor: "system"}
				if kind == "complaint change" {
					e.ChangeID = ch.ID
				} else {
					e.RunID = run.ID
				}
				if _, err := complaints.File(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			s.retention = recordRetention{}
			if _, err := s.Abandon(ctx, StopWork{ChangeID: ch.ID, Reason: "observation finished"}); err != nil {
				t.Fatal(err)
			}
			if err := s.PurgeAutomatedChanges(ctx); err != nil {
				t.Fatal(err)
			}
			_, err := s.Change(ctx, ch.ID)
			if kind == "unreferenced" {
				if err == nil {
					t.Fatal("unreferenced work survived")
				}
				return
			}
			if err != nil {
				t.Fatalf("referenced work was purged: %v", err)
			}
			if _, err := s.Run(ctx, run.ID); err != nil {
				t.Fatal("referenced run was purged: ", err)
			}
			if kind == "decision" {
				r, err := records.Get(person, recordID)
				if err != nil || r.ADR.ChangeNumber != ch.Number {
					t.Fatalf("decision context: %+v %v", r, err)
				}
			} else {
				rs, err := complaints.All(ctx, 10)
				if err != nil || len(rs) != 1 {
					t.Fatalf("complaints: %+v %v", rs, err)
				}
				if kind == "complaint change" && rs[0].ChangeNumber != ch.Number || kind == "complaint run" && rs[0].RunNumber != run.Number {
					t.Fatalf("complaint lost context: %+v", rs[0])
				}
			}
		})
	}
}

func TestQueueStartRetainsTheTargetsOfAFailedProducer(t *testing.T) {
	s, ch, _, _, _ := automatedFixture(t)
	run := admitAutomatedRun(t, s, ch, "agent/pi")
	steps, err := s.RunSteps(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	e := &Execution{service: s, ctx: t.Context(), step: steps[0], run: run}
	claim, err := newStepAttempts(s.db, func(string) bool { return true }).start(t.Context(), steps[0], "agent/pi", ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	e.claim = claim
	targets := []check.Finding{{Source: "osv", Identifiers: []string{"CVE-example"}, Package: &check.PackageRef{Name: "example", Version: "1.0.0", Ecosystem: "npm"}}}
	if err := e.StartFix(targets); err != nil {
		t.Fatal(err)
	}
	outcome, err := EncodeOutcome(StatusError, map[string]any{"reason": "producer failed before routine"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (reworkStore{db: s.db}).fail(t.Context(), claim, outcome); err != nil {
		t.Fatal(err)
	}
	records, err := workflowsql.New(s.db.SQL()).QueueAttempt(t.Context(), ch.ID)
	if err != nil || len(records) != 1 {
		t.Fatalf("retained attempt: %+v %v", records, err)
	}
	var got []check.Finding
	if err := json.Unmarshal(records[0].Targets, &got); err != nil || !reflect.DeepEqual(got, targets) || !records[0].AttemptedAt.Valid || !records[0].FailedAttempt {
		t.Fatalf("lost attempted targets: %+v %v", records[0], err)
	}
}

type scheduledScans struct{}

func (scheduledScans) ScansSource(context.Context, ids.ID) (bool, error) { return true, nil }

func (scheduledScans) ScansSourceFrom(context.Context, ids.ID, ProcedureFacts) (bool, error) {
	return true, nil
}

// Judge infrastructure faults retry through the normal writer twice; error
// evidence survives each reset and only the final attempt holds the queue.
func TestQueueCheckFaultRetriesTwiceBeforeHolding(t *testing.T) {
	for _, ownFault := range []bool{true, false} {
		t.Run(fmt.Sprint(ownFault), func(t *testing.T) {
			s, ch, _, _, _ := automatedFixture(t)
			run := admitAutomatedRun(t, s, ch, "routine")
			for attempt := 1; attempt <= 3; attempt++ {
				steps, err := s.RunSteps(t.Context(), run.ID)
				if err != nil {
					t.Fatal(err)
				}
				claim, err := newStepAttempts(s.db, func(string) bool { return true }).start(t.Context(), steps[0], "routine", ch.ProjectID)
				if err != nil {
					t.Fatal(err)
				}
				if attempt == 1 {
					e := &Execution{service: s, ctx: t.Context(), step: steps[0], run: run, claim: claim}
					if err := e.StartFix([]check.Finding{{Source: "osv", Identifiers: []string{"CVE-example"}, Package: &check.PackageRef{Name: "example"}}}); err != nil {
						t.Fatal(err)
					}
				}
				outcome, err := (CheckEvidence{Checks: []CheckReceipt{{Check: "routine", Verdict: VerdictError, AppTownFault: ownFault}}}).Outcome(StatusError, map[string]any{"reason": "no valid verdict"})
				if err != nil {
					t.Fatal(err)
				}
				result, err := (reworkStore{db: s.db}).fail(t.Context(), claim, outcome)
				if err != nil {
					t.Fatal(err)
				}
				retried := ownFault && attempt < 3
				if result.retried != retried || (result.held == nil) != retried {
					t.Fatalf("attempt %d: %+v", attempt, result)
				}
				rows, err := workflowsql.New(s.db.SQL()).QueueAttempt(t.Context(), ch.ID)
				if err != nil || len(rows) != 1 {
					t.Fatalf("queue: %+v %v", rows, err)
				}
				if rows[0].FailedAttempt == retried {
					t.Fatalf("attempt %d failure counted: %+v", attempt, rows[0])
				}
				if !retried {
					break
				}
			}
			receipts, err := workflowsql.New(s.db.SQL()).ReadRunEvents(t.Context(), workflowsql.ReadRunEventsParams{RunID: run.ID})
			if err != nil {
				t.Fatal(err)
			}
			retries := 0
			for _, r := range receipts {
				if r.Kind != "step.retried" {
					continue
				}
				retries++
				var saved retryReceipt
				if err := json.Unmarshal(r.PayloadJson, &saved); err != nil {
					t.Fatal(err)
				}
				if saved.By != "engine" || saved.Reason != "the check could not decide; retried" || !strings.Contains(string(saved.PreviousEvidence), "no valid verdict") {
					t.Fatalf("lost retry attribution/evidence: %+v", saved)
				}
			}
			want := 0
			if ownFault {
				want = 2
			}
			if retries != want {
				t.Fatalf("retries %d want %d", retries, want)
			}
		})
	}
}

// A person's exception promotes the Change; its later acceptance stays on Janitor.
func TestQueueKeepsPromotedChangeThroughOverrideAndAcceptance(t *testing.T) {
	s, ch, _, _, _ := automatedFixture(t)
	run := admitAutomatedRun(t, s, ch, "routine", "tests", "approval")
	steps, err := s.RunSteps(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var routine, tests, accept *Step
	for _, step := range steps {
		if step.Type == "routine" {
			routine = step
		} else if step.Type == "tests" {
			tests = step
		} else {
			accept = step
		}
	}
	claim, err := newStepAttempts(s.db, func(string) bool { return true }).start(t.Context(), routine, "routine", ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	e := &Execution{service: s, ctx: t.Context(), step: routine, run: run, claim: claim}
	if err := e.StartFix(nil); err != nil {
		t.Fatal(err)
	}
	outcome, err := EncodeOutcome(StatusFail, map[string]string{"reason": "dockerfile [routine:1] restructure. More detail."}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (reworkStore{db: s.db}).fail(t.Context(), claim, outcome); err != nil {
		t.Fatal(err)
	}
	before, err := s.QueueState(t.Context(), ch.ProjectID, "security")
	if err != nil || before.State != "decide" || before.Decide == nil || before.Decide.Act != nextOverride || before.Decide.StepID != routine.ID || before.Words != "Dockerfile restructure." {
		t.Fatalf("held: %+v %v", before, err)
	}
	ctx := access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{Kind: "user", Name: "verified-person", Via: "session"}})
	if _, err := s.Override(ctx, Override{StepID: routine.ID, Reason: "Keep the Dockerfile restructure"}); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	testClaim, err := newStepAttempts(s.db, func(string) bool { return true }).start(ctx, tests, "tests", ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := newStepAttempts(s.db, func(string) bool { return true }).finish(ctx, testClaim, StepOutcome{Status: StatusPass}); err != nil {
		t.Fatal(err)
	}
	after, err := s.QueueState(ctx, ch.ProjectID, "security")
	if err != nil || after.ChangeID != ch.ID || after.State != "decide" || after.Decide == nil || after.Decide.Act != NextApprove || after.Decide.StepID != accept.ID || after.Words != "Waiting for your acceptance" {
		t.Fatalf("after override: %+v %v", after, err)
	}
	if _, err := s.Approve(ctx, Approval{StepID: accept.ID, Decision: VoteApprove}); err != nil {
		t.Fatal(err)
	}
	// A later scheduled proposal must not close the human-touched Change.
	created, err := database.Transact(ctx, s.db, func(tx *database.Tx) (CreatedChange, error) {
		return CreateChangeIn(access.WithCaller(ctx, access.Caller{Verified: true, Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule", ProjectID: ch.ProjectID}}), tx, ChangeDraft{ProjectID: ch.ProjectID, Title: "next scan", Queue: "security"}, "apptown-system", time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	next := admitAutomatedRun(t, s, created.Change, "script")
	if err := s.replaceUntouched(ctx, next); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeAutomatedChanges(ctx); err != nil {
		t.Fatal(err)
	}
	kept, err := s.Change(ctx, ch.ID)
	if err != nil || !kept.State.canRun() {
		t.Fatalf("touched Change replaced: %+v %v", kept, err)
	}
}

// A recorded empty source proposal is different from unobserved files or a pack target.
func TestQueueDescribesOnlyRecordedProposals(t *testing.T) {
	for _, kind := range []string{"unobserved", "empty", "packs", "promoted"} {
		t.Run(kind, func(t *testing.T) {
			s, ch, _, _, _ := automatedFixture(t)
			if kind == "packs" {
				created, err := database.Transact(t.Context(), s.db, func(tx *database.Tx) (CreatedChange, error) {
					ctx := access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule", ProjectID: ch.ProjectID}})
					created, err := CreateChangeIn(ctx, tx, ChangeDraft{ProjectID: ch.ProjectID, Type: "uplift", Title: "Adopt packs", Queue: "security"}, "apptown-system", time.Now())
					if err != nil {
						return created, err
					}
					target, err := (testTarget{digest: "packs"}).RecordTargetIn(ctx, tx, TargetChange{ProjectID: ch.ProjectID, ChangeID: created.Change.ID, Actor: "apptown-system"})
					if err != nil {
						return created, err
					}
					_, err = workflowsql.New(tx.SQL()).AttachUpliftTarget(ctx, workflowsql.AttachUpliftTargetParams{ID: created.Change.ID, ProjectID: ch.ProjectID, TargetAdoptionID: target.ID, TargetCandidateJson: []byte(`{"packs":[{"pack":"apptown","version":2}]}`)})
					created.Change.TargetAdoptionID = target.ID
					return created, err
				})
				if err != nil {
					t.Fatal(err)
				}
				ch = created.Change
			}
			run := admitAutomatedRun(t, s, ch, "routine")
			steps, err := s.RunSteps(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := newStepAttempts(s.db, func(string) bool { return true }).start(t.Context(), steps[0], "routine", ch.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			e := &Execution{service: s, ctx: t.Context(), step: steps[0], run: run, claim: claim}
			if err := e.StartFix(nil); err != nil {
				t.Fatal(err)
			}
			if kind == "empty" {
				if _, _, err := e.keepProposal("recorded-empty-source", nil); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "packs" {
				if _, _, err := e.KeepPackProposal("main", "candidate"); err != nil {
					t.Fatal(err)
				}
			}
			outcome, err := EncodeOutcome(StatusFail, map[string]string{"reason": "a report is missing. [report:1] More detail."}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := (reworkStore{db: s.db}).fail(t.Context(), claim, outcome); err != nil {
				t.Fatal(err)
			}
			if kind == "promoted" {
				ctx := access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{Kind: "user", Name: "verified-person", Via: "session"}})
				if _, err := s.Override(ctx, Override{StepID: steps[0].ID, Reason: "Accept the report"}); err != nil {
					t.Fatal(err)
				}
				s.Wait()
			}
			read, err := s.QueueState(t.Context(), ch.ProjectID, "security")
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "empty":
				if read.ChangeSummary != "Makes no change" {
					t.Fatalf("empty: %+v", read)
				}
			case "packs":
				if read.ChangeSummary != "Updates ways of working: apptown" {
					t.Fatalf("packs: %+v", read)
				}
			case "unobserved":
				if read.ChangeSummary != "" {
					t.Fatalf("unobserved: %+v", read)
				}
			case "promoted":
				if read.Decide != nil || read.Words != "Waiting for a decision on the change" {
					t.Fatalf("promoted: %+v", read)
				}
			}
			if kind != "promoted" && read.Words != "A report is missing." {
				t.Fatalf("words: %+v", read)
			}
		})
	}
}

func TestQueueDecisionReasonFallbacks(t *testing.T) {
	for _, tc := range []struct {
		reason string
		status StepState
		want   string
	}{
		{`{"files":[]}`, StatusFail, "A check did not pass"},
		{`[evidence:1]`, StatusError, "A check could not finish"},
		{"scan could not finish. [scan:1] More detail.", StatusError, "Scan could not finish."},
	} {
		raw, err := json.Marshal(map[string]string{"reason": tc.reason})
		if err != nil {
			t.Fatal(err)
		}
		if got := decisionReason(&Step{Evidence: raw, Status: tc.status}); got != tc.want {
			t.Fatalf("%q: %q want %q", tc.reason, got, tc.want)
		}
	}
}
