package workflowexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/progress"
	"github.com/flokkit/apptown/stock"
)

func TestTargetScanMustWitnessTheFindingGone(t *testing.T) {
	for _, mode := range []string{"remaining", "not applicable", "detached", "fixed", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{{Name: "source", Uses: "scan", With: map[string]any{"on": "source", "scanner": "osv", "failOn": "critical"}}}}}}
			f, _ := newNodeFixFixture(t, "target", spec)
			applies := []string{"repository"}
			if mode == "not applicable" {
				applies = []string{"tool:ruby"}
			}
			cfg, _ := json.Marshal(map[string]any{"image": "scanner:test", "command": []string{"scan"}, "target": "source", "format": "osv", "report": ".apptown/osv.json", "kind": "vuln", "appliesTo": applies})
			if _, err := integration.NewWithClock(f.db, nil).Upsert(f.ctx, "osv", "scanner", "tool", string(cfg), true); err != nil {
				t.Fatal(err)
			}
			scanners := verification.NewScanners(verification.ScannerDependencies{DB: f.db, Evaluator: verification.NewEvaluator(targetScanEffects{}, nil, stock.Versions())})
			directory := identity.New(f.db, identity.Config{Projects: project.NewCatalogue(f.db)})
			conformanceService := conformance.New(f.db, access.Checked{Authority: directory, Projects: project.New(f.db, directory)}, nil, nil, nil)
			scan := verification.NewScanSteps(verification.ScanDependencies{DB: f.db, Scanners: scanners, Conformance: conformanceService, DataDir: f.dir, Progress: &progress.Feeds{}, Executor: verification.Executor{Run: func(context.Context, execution.Request) execution.Outcome {
				report := `{"results":[]}`
				if mode == "remaining" {
					report = `{"results":[{"packages":[{"package":{"name":"example","version":"1.0.0","ecosystem":"npm"},"vulnerabilities":[{"id":"CVE-example","database_specific":{"severity":"LOW"}}]}]}]}`
				}
				if mode == "malformed" {
					report = `no report`
				}
				return execution.Outcome{Termination: execution.Exited, Report: []byte(report)}
			}}})
			f.scan = scan.RunScan
			ch, err := f.work.Create(f.ctx, workflow.ChangeDraft{ProjectID: f.projectID, Title: "targeted fix"})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.work.PrepareWorkspace(f.ctx, ch.ID); err != nil {
				t.Fatal(err)
			}
			source := "osv"
			if mode == "detached" {
				source = "removed-scanner"
			}
			target, _ := json.Marshal([]check.Finding{{Source: source, Identifiers: []string{"CVE-example"}, Package: &check.PackageRef{Name: "example", Ecosystem: "npm", Version: "1.0.0"}}})
			inputs, _ := json.Marshal(map[string]string{"targets": string(target)})
			f.work.Stop()
			run, err := f.work.StartRun(f.ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "target", Inputs: inputs})
			if !errors.Is(err, workflow.ErrDispatchStopped) {
				t.Fatal(err)
			}
			sourceRef, err := workflow.ReadSourceIn(t.Context(), f.db, workflow.RunRef{ID: run.ID, ProjectID: f.projectID})
			if err != nil {
				t.Fatal(err)
			}
			workspace, err := sourceRef.Workspace(f.dir)
			if err != nil {
				t.Fatal(err)
			}
			updateWrite(t, workspace, "candidate.txt", "changed candidate")
			updateCommit(t, workspace, "candidate")
			if _, err := f.branches.Publish(t.Context(), scm.Publication{ProjectID: f.projectID, Repository: f.hub, Branch: sourceRef.Branch, From: workspace, Grant: scm.Grant{Holder: scm.RunHolder(run.ID)}}); err != nil {
				t.Fatal(err)
			}
			steps := queueSteps(t, f, run)
			var step *workflow.Step
			for _, s := range steps {
				if s.Type == "scan" {
					step = s
				}
			}
			queueExecute(t, f, step)
			status, ev := stepStatus(t, f, step.ID)
			want := workflow.StatusError
			if mode == "remaining" {
				want = workflow.StatusFail
			}
			if mode == "fixed" {
				want = workflow.StatusPass
			}
			if mode == "fixed" || mode == "remaining" || mode == "malformed" || mode == "not applicable" {
				heads, err := conformance.ScanObservationHeadsIn(t.Context(), f.db, f.projectID)
				if err != nil || len(heads) != 1 || heads[0].Subject.Tree == "" || heads[0].Subject.Commit == "" {
					t.Fatalf("source identity: %+v %v", heads, err)
				}
				source, err := workflow.ReadSourceIn(t.Context(), f.db, workflow.RunRef{ID: run.ID, ProjectID: f.projectID})
				if err != nil {
					t.Fatal(err)
				}
				repo, err := source.Repository(f.dir)
				if err != nil {
					t.Fatal(err)
				}
				tree, err := gitexec.Run(t.Context(), repo, "rev-parse", heads[0].Subject.Commit+"^{tree}")
				if err != nil || heads[0].Subject.Tree != strings.TrimSpace(tree) {
					t.Fatalf("scanned tree: %+v want %s: %v", heads[0], tree, err)
				}
			}
			if status != want {
				t.Fatalf("%s: got %s want %s: %+v", mode, status, want, ev)
			}
		})
	}
}

// The base holds the same target. Routine severity and baseline subtraction
// would pass it, so neither may witness that a targeted fix succeeded.
type targetScanEffects struct{}

func (targetScanEffects) Run(context.Context, execution.Request) execution.Outcome {
	return execution.Outcome{Termination: execution.Exited, Report: []byte(`{"results":[{"packages":[{"package":{"name":"example","version":"1.0.0","ecosystem":"npm"},"vulnerabilities":[{"id":"CVE-example"}]}]}]}`)}
}
func (targetScanEffects) Observe(context.Context, ids.ID, string, string) (conformance.Outcome, string) {
	return conformance.DoesNotConform, "base target"
}

// The scan step records the immutable bundle subjects through its live attempt;
// reading Conformance afterwards proves they survive independently of the step.
func TestBundleScanRetainsTheRunBundleSubjects(t *testing.T) {
	for _, mode := range []string{"digests", "local IDs", "no bundle"} {
		t.Run(mode, func(t *testing.T) {
			spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "ship", Steps: []schema.Step{{Name: "scan-bundle", Uses: "scan", With: map[string]any{"on": "bundle"}}}}}}
			f, _ := newNodeFixFixture(t, "bundle-scan", spec)
			cfg := `{"image":"scanner:test","command":["scan","${IMAGE_TAR}"],"target":"image"}`
			if _, err := integration.NewWithClock(f.db, nil).Upsert(f.ctx, "image-scanner", "scanner", "tool", cfg, true); err != nil {
				t.Fatal(err)
			}
			directory := identity.New(f.db, identity.Config{Projects: project.NewCatalogue(f.db)})
			owner := conformance.New(f.db, access.Checked{Authority: directory, Projects: project.New(f.db, directory)}, nil, nil, nil)
			bundles := &scanBundleFake{project: f.projectID}
			if mode != "no bundle" {
				bundles.bundle = &deployment.ReleaseBundle{ID: ids.New(time.Now()), Parts: map[string]string{"web": "registry.example/p@sha256:" + strings.Repeat("a", 64)}, Init: map[string]string{"db": "registry.example/p@sha256:" + strings.Repeat("b", 64)}, BuiltFrom: deployment.BundleOrigin{Registry: "registry.example"}}
				if mode == "local IDs" {
					bundles.bundle.BuiltFrom.Registry = ""
				}
			}
			pulled, scanned := map[string]int{}, map[string]int{}
			scan := verification.NewScanSteps(verification.ScanDependencies{DB: f.db, Bundles: bundles, Scanners: verification.NewScanners(verification.ScannerDependencies{DB: f.db}), Conformance: owner, Integrations: scanRegistry{}, Progress: &progress.Feeds{}, DataDir: f.dir, Executor: verification.Executor{
				Image: func(_ context.Context, r execution.ImageRequest) execution.ImageAnswer {
					pulled[r.Pull]++
					if r.Registry == nil || r.Registry.Username != "robot" || r.AllowBuild {
						t.Fatalf("pull: %+v", r)
					}
					return execution.ImageAnswer{ID: "local"}
				},
				Run: func(_ context.Context, r execution.Request) execution.Outcome {
					scanned[r.ExportImage]++
					return execution.Outcome{Termination: execution.Exited}
				},
			}})
			f.scan = scan.RunScan
			ch, err := f.work.Create(f.ctx, workflow.ChangeDraft{ProjectID: f.projectID, Title: "Observe the release bundle"})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.work.PrepareWorkspace(f.ctx, ch.ID); err != nil {
				t.Fatal(err)
			}
			f.work.Stop()
			run, err := f.work.StartRun(f.ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "bundle-scan"})
			if !errors.Is(err, workflow.ErrDispatchStopped) {
				t.Fatal(err)
			}
			bundles.run = run.ID
			steps := queueSteps(t, f, run)
			if len(steps) != 1 || steps["scan-bundle"] == nil {
				t.Fatalf("expanded bundle scan: %+v", steps)
			}
			queueExecute(t, f, steps["scan-bundle"])
			status, _ := stepStatus(t, f, steps["scan-bundle"].ID)
			heads, err := conformance.ScanObservationHeadsIn(t.Context(), f.db, f.projectID)
			if err != nil {
				t.Fatal(err)
			}
			var selected []ids.ID
			for _, h := range heads {
				selected = append(selected, h.ID)
			}
			observations, err := conformance.ScanObservationsByIDIn(t.Context(), f.db, f.projectID, selected)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "digests" {
				if status != workflow.StatusNotApplicable || len(observations) != 1 || observations[0].Subject.On != "bundle" || observations[0].Reason == "" || len(pulled) != 0 {
					t.Fatalf("not applicable: %s %+v pulls=%v", status, observations, pulled)
				}
				return
			}
			if status != workflow.StatusPass || len(observations) != 2 {
				t.Fatalf("bundle: %s %+v", status, observations)
			}
			for _, o := range observations {
				want := bundles.bundle.Parts[o.Subject.Part]
				if strings.HasPrefix(o.Subject.Part, "init/") {
					want = bundles.bundle.Init[strings.TrimPrefix(o.Subject.Part, "init/")]
				}
				if o.Subject.On != "bundle" || o.Subject.BundleID != bundles.bundle.ID || !o.Subject.DeploymentID.IsZero() || o.Subject.Digest != want || want == "" || pulled[want] != 1 || scanned[want] != 1 || o.Scanner != "image-scanner" || o.Attempt == 0 {
					t.Fatalf("wrong retained subject: %+v", o)
				}
			}
		})
	}
}

type scanBundleFake struct {
	project, run ids.ID
	bundle       *deployment.ReleaseBundle
}

func (f *scanBundleFake) OfRun(_ context.Context, project, run ids.ID) (*deployment.ReleaseBundle, error) {
	if project != f.project || run != f.run {
		return nil, fmt.Errorf("wrong bundle scope")
	}
	return f.bundle, nil
}

type scanRegistry struct{}

func (scanRegistry) Registry(context.Context, ids.ID) (*integration.Registry, error) {
	return &integration.Registry{Host: "registry.example", Username: "robot", Token: "token"}, nil
}

func TestImageTargetPassesSourceAndRequiresCandidateImageAssessment(t *testing.T) {
	for _, mode := range []string{"remaining", "not applicable", "detached", "fixed", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{
				{Name: "source", Uses: "scan", With: map[string]any{"on": "source", "scanner": "osv"}},
				{Name: "image", Uses: "scan", With: map[string]any{"on": "image", "scanner": "trivy", "failOn": "critical"}},
			}}}}
			f, _ := newNodeFixFixture(t, "target", spec)
			applies := []string{"repository"}
			if mode == "not applicable" {
				applies = []string{"tool:ruby"}
			}
			cfg, _ := json.Marshal(map[string]any{"image": "scanner:test", "command": []string{"scan"}, "target": "image", "format": "osv", "report": ".apptown/osv.json", "kind": "vuln", "appliesTo": applies})
			if _, err := integration.NewWithClock(f.db, nil).Upsert(f.ctx, "trivy", "scanner", "tool", string(cfg), true); err != nil {
				t.Fatal(err)
			}
			sourceCfg := `{"image":"scanner:test","command":["scan"],"target":"source","format":"osv","report":".apptown/osv.json","kind":"vuln","appliesTo":["repository"]}`
			if _, err := integration.NewWithClock(f.db, nil).Upsert(f.ctx, "osv", "scanner", "tool", sourceCfg, true); err != nil {
				t.Fatal(err)
			}
			imageExecutor := verification.Executor{Run: targetScanEffects{}.Run, Image: func(context.Context, execution.ImageRequest) execution.ImageAnswer {
				return execution.ImageAnswer{ID: "sha256:fixture"}
			}}
			scanners := verification.NewScanners(verification.ScannerDependencies{DB: f.db, Deployments: deployment.NewDeployments(f.db), Executor: imageExecutor, Evaluator: verification.NewEvaluator(targetScanEffects{}, nil, stock.Versions())})
			directory := identity.New(f.db, identity.Config{Projects: project.NewCatalogue(f.db)})
			conformanceService := conformance.New(f.db, access.Checked{Authority: directory, Projects: project.New(f.db, directory)}, nil, nil, nil)
			scan := verification.NewScanSteps(verification.ScanDependencies{DB: f.db, Scanners: scanners, Conformance: conformanceService, DataDir: f.dir, Progress: &progress.Feeds{}, Executor: verification.Executor{Run: func(_ context.Context, req execution.Request) execution.Outcome {
				report := `{"results":[]}`
				if mode == "remaining" && req.ExportImage != "" {
					report = `{"results":[{"packages":[{"package":{"name":"example","version":"1.0.0","ecosystem":"npm"},"vulnerabilities":[{"id":"CVE-example","database_specific":{"severity":"LOW"}}]}]}]}`
				}
				if mode == "malformed" && req.ExportImage != "" {
					report = `no report`
				}
				return execution.Outcome{Termination: execution.Exited, Report: []byte(report)}
			}}})
			f.scan = scan.RunScan
			ch, err := f.work.Create(f.ctx, workflow.ChangeDraft{ProjectID: f.projectID, Title: "targeted fix"})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.work.PrepareWorkspace(f.ctx, ch.ID); err != nil {
				t.Fatal(err)
			}
			source := "trivy"
			if mode == "detached" {
				source = "removed-scanner"
			}
			target, _ := json.Marshal([]check.Finding{{ScanPoint: "image", Source: source, Identifiers: []string{"CVE-example"}, Package: &check.PackageRef{Name: "example", Ecosystem: "npm", Version: "1.0.0"}}})
			inputs, _ := json.Marshal(map[string]string{"targets": string(target)})
			f.work.Stop()
			run, err := f.work.StartRun(f.ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "target", Inputs: inputs})
			if !errors.Is(err, workflow.ErrDispatchStopped) {
				t.Fatal(err)
			}
			sourceRef, err := workflow.ReadSourceIn(t.Context(), f.db, workflow.RunRef{ID: run.ID, ProjectID: f.projectID})
			if err != nil {
				t.Fatal(err)
			}
			workspace, err := sourceRef.Workspace(f.dir)
			if err != nil {
				t.Fatal(err)
			}
			updateWrite(t, workspace, "candidate.txt", "changed candidate")
			updateCommit(t, workspace, "candidate")
			if _, err := f.branches.Publish(t.Context(), scm.Publication{ProjectID: f.projectID, Repository: f.hub, Branch: sourceRef.Branch, From: workspace, Grant: scm.Grant{Holder: scm.RunHolder(run.ID)}}); err != nil {
				t.Fatal(err)
			}
			steps := queueSteps(t, f, run)
			queueExecute(t, f, steps["source:osv"])
			if status, ev := stepStatus(t, f, steps["source:osv"].ID); status != workflow.StatusPass {
				t.Fatalf("image target refused by source scan: %s %+v", status, ev)
			}
			step := steps["image:trivy"]
			queueExecute(t, f, step)
			status, ev := stepStatus(t, f, step.ID)
			want := workflow.StatusError
			if mode == "remaining" {
				want = workflow.StatusFail
			}
			if mode == "fixed" {
				want = workflow.StatusPass
			}
			if status != want {
				t.Fatalf("%s: got %s want %s: %+v", mode, status, want, ev)
			}
		})
	}
}

// Extend the empty-result fixture through the shipped scan and its real child
// call. The scanner still has the target; no empty candidate can clear it.
func TestEmptySecurityFixClosesAndOffersTargetsAgain(t *testing.T) {
	for _, rebuildStatus := range []workflow.StepState{workflow.StatusPass, workflow.StatusNotApplicable} {
		t.Run(rebuildStatus.String(), func(t *testing.T) {
			t.Parallel()
			raw, err := stock.FS.ReadFile("janitor-scan.yaml")
			if err != nil {
				t.Fatal(err)
			}
			doc, err := schema.ParseWorkflow(raw)
			if err != nil {
				t.Fatal(err)
			}
			f, _ := newNodeFixFixture(t, "janitor-scan", doc.Spec, func(d *workflow.Dependencies) {
				d.Runners["rebuild"] = func(task *workflow.Execution) {
					if _, err := task.Claim(); err != nil {
						t.Error(err)
						return
					}
					task.Settle(workflow.EncodeOutcome(rebuildStatus, map[string]any{"reason": "rebuild settled"}, nil))
				}
				d.Automatic = append(d.Automatic, "rebuild")
			})
			raw, err = stock.FS.ReadFile("janitor-fix.yaml")
			if err != nil {
				t.Fatal(err)
			}
			childDoc, err := schema.ParseWorkflow(raw)
			if err != nil {
				t.Fatal(err)
			}
			f.definitions["janitor-fix"] = childDoc.Spec
			updateWrite(t, f.hub, "Dockerfile", "# `app` here\nFROM python:3.12-slim\n")
			updateCommit(t, f.hub, "runtime image")
			cfg := `{"image":"scanner:test","command":["scan","${IMAGE_TAR}"],"target":"image","format":"osv","report":".apptown/osv.json","kind":"vuln","appliesTo":["repository"]}`
			if _, err := integration.NewWithClock(f.db, nil).Upsert(f.ctx, "trivy", "scanner", "tool", cfg, true); err != nil {
				t.Fatal(err)
			}
			imageExecutor := verification.Executor{Run: targetScanEffects{}.Run, Image: func(context.Context, execution.ImageRequest) execution.ImageAnswer {
				return execution.ImageAnswer{ID: "sha256:fixture"}
			}}
			scanners := verification.NewScanners(verification.ScannerDependencies{DB: f.db, Executor: imageExecutor, Evaluator: verification.NewEvaluator(targetScanEffects{}, nil, stock.Versions())})
			directory := identity.New(f.db, identity.Config{Projects: project.NewCatalogue(f.db)})
			owner := conformance.New(f.db, access.Checked{Authority: directory, Projects: project.New(f.db, directory)}, nil, nil, nil)
			scans := verification.NewScanSteps(verification.ScanDependencies{DB: f.db, Scanners: scanners, Conformance: owner, DataDir: f.dir, Progress: &progress.Feeds{}, Executor: imageExecutor})
			finding := check.Finding{Fingerprint: "image-target", Source: "trivy", Severity: "high", Identifiers: []string{"CVE-example"}, Package: &check.PackageRef{Name: "example", Ecosystem: "npm", Version: "1.0.0"}}
			f.scan = func(task *workflow.Execution) {
				if task.Run().WorkflowName == "janitor-fix" {
					scans.RunScan(task)
					return
				}
				if _, err := task.Claim(); err != nil {
					t.Error(err)
					return
				}
				if strings.HasPrefix(task.Step().Name, "scan-running") {
					if err := task.InAttempt("scan", func(tx *database.Tx, attempt int64) error {
						return conformance.RecordScanObservationIn(task.Context(), tx, conformance.ScanObservation{ProjectID: f.projectID, ChangeID: task.Run().ChangeID, RunID: task.Run().ID, StepID: task.Step().ID, Attempt: attempt, Subject: conformance.ScanSubject{On: "running", Part: "web", Digest: "sha256:fixture"}, Scanner: "trivy", Verdict: "fail", Reason: "target remains", ObservedAt: time.Now(), Findings: conformance.WrapFindings([]check.Finding{finding})})
					}); err != nil {
						t.Error(err)
						return
					}
					task.Settle(workflow.EncodeOutcome(workflow.StatusFail, map[string]any{"reason": "target remains"}, nil))
				} else {
					task.Settle(workflow.EncodeOutcome(workflow.StatusNotApplicable, map[string]any{"reason": "no source scanner"}, nil))
				}
			}
			const report = "Waiting for a newer python:3.12-slim build: the newest build still has CVE-example. Registry listing: python:3.12-slim is current."
			agentCalls := 0
			installFakePi(f, func(_ int, _ execution.Request) execution.Outcome {
				agentCalls++
				return execution.Outcome{Termination: execution.Exited, Output: piSays(report)}
			})
			f.commands.judge = updateJudgeFunc(func(_ context.Context, _ workflow.Source, item check.Item, diff string) (workflow.CheckReceipt, error) {
				if diff != "" || !strings.Contains(item.Rubric, report) {
					t.Fatalf("empty report not judged: %s, diff %s", item.Rubric, diff)
				}
				return workflow.CheckReceipt{Verdict: workflow.VerdictPass, Detail: "no newer build fixes the finding"}, nil
			})
			f.checks = func(execution.Request) execution.Outcome { return execution.Outcome{Termination: execution.Exited} }
			f.preview = func(task *workflow.Execution) {
				if _, err := task.Claim(); err == nil {
					task.Settle(workflow.EncodeOutcome(workflow.StatusPass, nil, nil))
				}
			}
			for night := 1; night <= 2; night++ {
				root := queueRun(t, f, "janitor-scan", "security")
				steps := queueSteps(t, f, root)
				for _, name := range []string{"scan-source", "scan-running", "rebuild", "fix"} {
					queueExecute(t, f, steps[name])
				}
				status, ev := stepStatus(t, f, steps["fix"].ID)
				if status != workflow.StatusRunning {
					t.Fatalf("night %d target not offered: %s %+v", night, status, ev)
				}
				var childID ids.ID
				body, _ := json.Marshal(ev["childRunId"])
				if err := json.Unmarshal(body, &childID); err != nil {
					t.Fatal(err)
				}
				child, err := f.work.Run(f.ctx, childID)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(child.InputsJSON), "CVE-example") {
					t.Fatalf("target lost: %s", child.InputsJSON)
				}
				childSteps := queueSteps(t, f, child)
				for _, name := range []string{"update", "routine", "tests", "boot", "scan-source", "scan-image:trivy"} {
					queueExecute(t, f, childSteps[name])
				}
				for _, name := range []string{"scan-image:trivy", "scan-source"} {
					if status, ev := stepStatus(t, f, childSteps[name].ID); status != workflow.StatusNotApplicable || ev["reason"] != "the change is empty; the scan of main stands" {
						t.Fatalf("empty candidate scan %s: %s %+v", name, status, ev)
					}
				}
				if _, err := f.work.Advance(f.ctx, child.ID); err != nil {
					t.Fatal(err)
				}
				if status, ev := stepStatus(t, f, steps["fix"].ID); status != workflow.StatusPass || ev["nothingToChange"] != true || !strings.Contains(fmt.Sprint(ev["reason"]), "Waiting for a newer") {
					t.Fatalf("child account lost: %s %+v", status, ev)
				}
				if _, err := f.work.Advance(f.ctx, root.ID); err != nil {
					t.Fatal(err)
				}
				change, err := workflow.ReadChangeIn(f.ctx, f.db, root.ChangeID)
				if err != nil || change.ClosedAs != workflow.ClosedNothingToChange {
					t.Fatalf("empty security fix stayed open: %+v %v", change, err)
				}
				for _, step := range queueSteps(t, f, child) {
					if step.Name == "repair" && (step.Status != workflow.StatusNotApplicable || step.StartedAt.Valid) {
						t.Fatalf("repair requested: %+v", step)
					}
				}
				accept := queueSteps(t, f, root)["accept"]
				if accept.Status != workflow.StatusPending || accept.StartedAt.Valid {
					t.Fatalf("person asked: %+v", accept)
				}
				attempts, err := f.work.Attempts(f.ctx, f.projectID, "security", 10)
				if err != nil || len(attempts) != night || attempts[0].Outcome != "fail" || !strings.Contains(attempts[0].Reason, "target remains") {
					t.Fatalf("attempt verdict lost: %+v %v", attempts, err)
				}
				heads, err := conformance.ScanObservationHeadsIn(f.ctx, f.db, f.projectID)
				if err != nil {
					t.Fatal(err)
				}
				var selected []ids.ID
				for _, h := range heads {
					if h.RunID == root.ID {
						selected = append(selected, h.ID)
					}
				}
				observations, err := conformance.ScanObservationsByIDIn(f.ctx, f.db, f.projectID, selected)
				if err != nil || len(observations) != 1 || observations[0].Verdict != "fail" || len(observations[0].Findings) != 1 || observations[0].Findings[0].Fingerprint != finding.Fingerprint {
					t.Fatalf("observation lost: %+v %v", observations, err)
				}
			}
			if agentCalls != 2 {
				t.Fatalf("unexpected repair or missed retry: %d agent calls", agentCalls)
			}
		})
	}
}
