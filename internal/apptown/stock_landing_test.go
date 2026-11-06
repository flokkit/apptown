package apptown

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/ids"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

// Use the shipped definition and executor: moving a scanner across the merge
// boundary must fail this test even if stage-policy unit tests still pass.
func TestStockLandingImageFailureLeavesMainUntouchedAndApprovalUnavailable(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	a.Ways.SyncStockWorkflows(t.Context())
	pid, base, git := fleetRepo(t, a, "scan-before-merge", map[string]string{"a.txt": "seed"})
	if _, err := a.recordAdoption(a.projectByID(context.Background(), pid), "edit", "stock landing fixture", "fixture"); err != nil {
		t.Fatal(err)
	}
	attachScanner(t, a, "test-image", map[string]any{"image": "test:scanner", "command": []string{"scan"}, "report": ".apptown/report.json", "format": "sarif", "target": "image", "kind": "vulnerability"})
	rebuildWork(t, a, func(d *workflow.Dependencies) {
		d.Runners = map[string]workflow.Runner{}
		for _, kind := range []string{"reconcile", "tests", "scan", "guidance/conformance", "build"} {
			d.Runners[kind] = func(e *workflow.Execution) {
				step, err := e.Claim()
				if err != nil {
					t.Error(err)
					return
				}
				status := workflow.StatusPass
				if strings.HasPrefix(step.Name, "scan-image") {
					status = workflow.StatusFail
				}
				if step.Type == "build" {
					t.Error("deployment ran after a failed image gate")
				}
				if err := e.Finish(workflow.StepOutcome{Status: status}, nil); err != nil {
					t.Error(err)
				}
			}
		}
	})
	ch := engineChange(t, a, a.projectByID(context.Background(), pid))
	// A real branch difference makes an accidental merge observable.
	ws := filepath.Join(a.cfg.dataDir, "workspaces", "change-"+fmt.Sprint(ch.ID))
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("change"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, ws, "add", "a.txt")
	gittest.Run(t, ws, "commit", "-qm", "change")
	ctx := asLander(t, a)
	run, err := a.Work.RequestLanding(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	snap, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	for _, step := range snap.Steps {
		if strings.HasPrefix(step.Name, "scan-image") && step.Status == workflow.StatusFail {
			failed = true
		}
		if step.Type == "approval" || step.Type == "build" {
			t.Fatalf("post-gate step already exists: %+v", step)
		}
	}
	if !failed {
		t.Fatalf("image scan did not block stock landing: %+v", snap.Steps)
	}
	if got := strings.TrimSpace(git("rev-parse", a.projectByID(ctx, pid).DefaultBranch)); got != base {
		t.Fatalf("main moved to %s from %s", got, base)
	}
	stored, err := a.Work.Change(ctx, ch.ID)
	if err != nil || stored.State != workflow.ChangeLanding {
		t.Fatalf("change after failed scan: %+v %v", stored, err)
	}
	for _, event := range snap.Events {
		if event.Kind == "merge.completed" {
			t.Fatal("merge receipt exists after failed scan")
		}
	}
	if snap.Run.Status != workflow.RunRunning || snap.Run.CurrentStage != "gates" {
		t.Fatalf("blocked landing: %+v", snap.Run)
	}
}

// Stock landing approval supplies assent to merge. Its approver's grants never
// authorize deployment; every destination uses its own confirmed setting.
func TestStockLandingUsesFeedAuthorityAndSurvivesReleaseRefusals(t *testing.T) {
	for _, scenario := range []string{"confirmed", "revoked setter", "unconfirmed"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			a, _ := bootWorld(t)
			a.Ways.SyncStockWorkflows(t.Context())
			a.SeedTargets(t.Context())
			rebuildWork(t, a, func(d *workflow.Dependencies) {
				build := d.Runners["build"]
				d.Runners["build"] = func(e *workflow.Execution) { build(e); moveClock(a, 2*time.Second) }
			})
			pid, _, _ := fleetRepo(t, a, "stock-release", map[string]string{".apptown/app.yaml": "app:\n  web:\n    image: example/web:1\n    ports: [\"8080\"]\n    public: true\n", "a.txt": "seed"})
			project := a.projectByID(t.Context(), pid)
			if _, err := a.recordAdoption(project, "edit", "stock landing fixture", "fixture"); err != nil {
				t.Fatal(err)
			}
			testAdministrator(t, a, "admin")
			deployGrant, err := a.Access.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "setter", Capability: string(access.CapDeploymentDeploy), Scope: access.ScopeProject, ScopeID: pid.String(), By: "admin"}, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Access.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "setter", Capability: string(access.CapDeploymentBind), Scope: access.ScopeProject, ScopeID: pid.String(), By: "admin"}, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Access.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "only-approver", Role: access.RoleProjectApprover, Scope: access.ScopeProject, ScopeID: pid.String(), By: "admin"}, ""); err != nil {
				t.Fatal(err)
			}
			setter := access.Principal{ID: testPrincipalOf(t, a.DB, "setter"), Kind: "user", Name: "setter", Via: "header", Credential: identity.HeaderCredential("setter")}
			approver := access.Principal{ID: testPrincipalOf(t, a.DB, "only-approver"), Kind: "user", Name: "only-approver", Via: "header", Credential: identity.HeaderCredential("only-approver")}
			setterCtx := verifiedContext(t.Context(), setter)
			approverCtx := verifiedContext(t.Context(), approver)
			target, err := a.Deployment.Targets.ByName(t.Context(), deployment.BuiltInTargetName)
			if err != nil {
				t.Fatal(err)
			}
			var destination *deployment.Deployment
			if scenario == "unconfirmed" {
				// The migration deliberately records no consent. The migration suite
				// verifies this shape from the old schema; no authorized settings write
				// creates it, so use the owner's record operation for the upgrade fixture.
				destination, err = a.Deployment.Deployments.EnsureStanding(t.Context(), pid, "automatic", target.ID, deployment.Attributes{Feed: "main", Audience: "team", DataClass: "synthetic"})
			} else {
				configured, e := a.Deployment.Configure(setterCtx, deployment.ConfigureDeployment{ProjectID: pid, Name: "automatic", Target: target.Name, Attributes: deployment.Attributes{Feed: "main", Audience: "team", DataClass: "synthetic"}})
				err = e
				destination = configured.Deployment
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "confirmed" {
				if _, err := a.Deployment.Configure(setterCtx, deployment.ConfigureDeployment{ProjectID: pid, Name: "another", Target: target.Name, Attributes: deployment.Attributes{Feed: "main", Audience: "team", DataClass: "synthetic"}}); err != nil {
					t.Fatal(err)
				}
			}
			land := func(marker string) (*workflow.Run, []*deployment.Release) {
				t.Helper()
				ch := engineChange(t, a, project)
				ws := filepath.Join(a.cfg.dataDir, "workspaces", "change-"+ch.ID.String())
				if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte(marker), 0644); err != nil {
					t.Fatal(err)
				}
				gittest.Run(t, ws, "add", "a.txt")
				gittest.Run(t, ws, "commit", "-qm", marker)
				run, err := a.Work.RequestLanding(asLander(t, a), ch.ID)
				if err != nil {
					t.Fatal(err)
				}
				a.Work.Wait()
				snapshot, err := a.Work.Snapshot(approverCtx, run.ID, workflow.DetailView)
				if err != nil {
					t.Fatal(err)
				}
				var approval ids.ID
				for _, step := range snapshot.Steps {
					if step.Type == "approval" && step.Status == workflow.StatusPending {
						approval = step.ID
					}
				}
				if approval.IsZero() {
					t.Fatalf("stock landing did not reach approval: %+v", snapshot)
				}
				if _, err := a.Work.Approve(approverCtx, workflow.Approval{StepID: approval, Decision: workflow.VoteApprove, Adds: shownAdds(t, a, approverCtx, approval)}); err != nil {
					t.Fatal(err)
				}
				a.Work.Wait()
				snapshot, err = a.Work.Snapshot(approverCtx, run.ID, workflow.DetailView)
				if err != nil || snapshot.Run.Status != workflow.RunDone {
					t.Fatalf("stock landing: %+v %v", snapshot, err)
				}
				stored, err := a.Work.Change(approverCtx, ch.ID)
				if err != nil || stored.State != workflow.ChangeShipped {
					t.Fatalf("change: %+v %v", stored, err)
				}
				runQueuedJobForTest(t, a, workflowCompletionJob, fmt.Sprintf("%s/release", run.ID))
				requests, err := a.Deployment.Releases.LandingRequests(t.Context(), pid, ch.ID)
				if err != nil {
					t.Fatal(err)
				}
				carried, err := a.Deployment.Releases.ForChange(t.Context(), pid, ch.ID)
				if err != nil || len(carried) != len(requests) {
					t.Fatalf("landing change missing from release membership: %+v %v", carried, err)
				}
				return snapshot.Run, requests
			}
			run, requests := land("first landing")
			want := 1
			if scenario == "confirmed" {
				want = 2
			}
			if len(requests) != want {
				t.Fatalf("requests: %+v", requests)
			}
			if scenario == "revoked setter" {
				if ok, err := a.Access.Revoke(t.Context(), deployGrant, "admin"); err != nil || !ok {
					t.Fatalf("revoke: %t %v", ok, err)
				}
			}
			for _, request := range requests {
				metadata, err := a.Deployment.Releases.RequestMetadata(t.Context(), request.ID)
				if err != nil || metadata == nil || metadata.Approvers != "only-approver" {
					t.Fatalf("provenance: %+v %v", metadata, err)
				}
				err = a.Deployment.ExecuteRelease(t.Context(), request.ID)
				switch scenario {
				case "confirmed":
					if err != nil || metadata.SetBy != "setter" || metadata.Credential != setter.Credential {
						t.Fatalf("setting authority: %+v %v", metadata, err)
					}
				case "revoked setter":
					if err == nil || !strings.Contains(err.Error(), "confirm follows main again") {
						t.Fatalf("revoked authority: %v", err)
					}
				case "unconfirmed":
					if err == nil || !strings.Contains(err.Error(), "follows main is not confirmed for this deployment") {
						t.Fatalf("unconfirmed: %v", err)
					}
				}
				settled, readErr := a.Deployment.Releases.ByNumber(t.Context(), pid, request.Number)
				wantState := deployment.Released
				if scenario != "confirmed" {
					wantState = deployment.ReleaseFailed
				}
				if readErr != nil || settled.State != wantState {
					t.Fatalf("release settlement: %+v %v", settled, readErr)
				}
				if scenario != "confirmed" {
					events, e := a.Deployment.Releases.Events(t.Context(), request.ID)
					if e != nil || len(events) == 0 || !strings.Contains(events[len(events)-1].Text, err.Error()) {
						t.Fatalf("release lost refusal: %+v %v", events, e)
					}
				}
			}
			if stored, err := a.Work.Change(approverCtx, run.ChangeID); err != nil || stored.State != workflow.ChangeShipped {
				t.Fatalf("release changed landing: %+v %v", stored, err)
			}
			if scenario == "unconfirmed" {
				if _, err := a.Deployment.ConfirmFeed(setterCtx, pid, destination.Name, destination.FeedRevision); err != nil {
					t.Fatal(err)
				}
				_, next := land("after confirmation")
				if len(next) != 1 {
					t.Fatalf("next requests: %+v", next)
				}
				metadata, err := a.Deployment.Releases.RequestMetadata(t.Context(), next[0].ID)
				if err != nil || metadata == nil || metadata.SetBy != "setter" || metadata.Credential != setter.Credential {
					t.Fatalf("confirmed authority: %+v %v", metadata, err)
				}
				if err := a.Deployment.ExecuteRelease(t.Context(), next[0].ID); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
