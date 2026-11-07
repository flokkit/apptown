package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/ids"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
)

// Queue admission is not continuing permission to create managed databases
// and replace an application. Exercise the real identity owner and provider
// adapter, including a successful control using the same captured caller.
func TestQueuedReleaseRechecksTheRequestersCurrentAuthority(t *testing.T) {
	for _, scenario := range []string{"current", "grant revoked", "account disabled", "token revoked", "token ceiling widened", "renamed requester", "old name reused", "missing snapshot", "malformed snapshot", "snapshot without UUID"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			a, provider, p, bundle := doWorld(t)
			testAdministrator(t, a, "admin")
			if _, err := a.Access.EnsurePerson(t.Context(), "alice"); err != nil {
				t.Fatal(err)
			}
			target, err := a.Deployment.Targets.ByName(t.Context(), "do-syd")
			if err != nil {
				t.Fatal(err)
			}
			dep, err := a.Deployment.Deployments.EnsureStanding(t.Context(), p.ID, "cloud", target.ID, deployment.Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"})
			if err != nil {
				t.Fatal(err)
			}
			grant, err := a.Access.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "alice", Capability: string(access.CapDeploymentDeploy), Scope: access.ScopeProject, ScopeID: p.ID.String(), By: "admin"}, "")
			if err != nil {
				t.Fatal(err)
			}
			captured := access.Principal{ID: testPrincipalOf(t, a.DB, "alice"), Kind: "user", Name: "alice", Via: "header", Credential: identity.HeaderCredential("alice")}
			admin := access.Principal{ID: testPrincipalOf(t, a.DB, "admin"), Kind: "user", Name: "admin", Via: "session"}
			var presented string
			if scenario == "token revoked" || scenario == "token ceiling widened" {
				issued, err := a.Access.IssueToken(t.Context(), identity.NewToken{Name: "release-requester", User: "alice", Purpose: "cli", Role: "member"}, admin)
				if err != nil {
					t.Fatal(err)
				}
				presented = issued.Plaintext
				captured, err = a.Access.PersonalTokenIdentity(t.Context(), presented, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			permission := access.Permission{Capability: access.CapDeploymentDeploy, Resource: access.Resource{Type: "project", ID: p.ID.String(), ProjectID: p.ID}}
			if err := a.Access.RequireStill(t.Context(), captured, permission); err != nil {
				t.Fatalf("valid control authority: %v", err)
			}
			accepted, err := a.Deployment.RequestRelease(verifiedContext(t.Context(), captured), deployment.DeploySelection{ProjectID: p.ID, DeploymentName: dep.Name, BundleID: bundle.ID})
			if err != nil {
				t.Fatal(err)
			}
			success := scenario == "current" || scenario == "renamed requester"
			switch scenario {
			case "grant revoked":
				if changed, err := a.Access.Revoke(t.Context(), grant, "admin"); err != nil || !changed {
					t.Fatalf("revoke: %t %v", changed, err)
				}
			case "token revoked":
				if changed, err := a.Access.RevokeToken(t.Context(), "release-requester", admin); err != nil || !changed {
					t.Fatalf("revoke token: %t %v", changed, err)
				}
			case "token ceiling widened":
				if changed, err := a.Access.Revoke(t.Context(), grant, "admin"); err != nil || !changed {
					t.Fatalf("withdraw narrow grant: %t %v", changed, err)
				}
				testAdministrator(t, a, "alice")
				// sql-guarantee: no token command edits an issued ceiling;
				// model a broader current credential to verify that the
				// sql-guarantee: queued snapshot still limits it to its admitted ceiling.
				completionExec(t, a.DB, `UPDATE auth_tokens SET role='admin' WHERE name='release-requester'`)
				current, err := a.Access.PersonalTokenIdentity(t.Context(), presented, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := a.Access.RequireStill(t.Context(), current, permission); err != nil {
					t.Fatalf("broader current-credential control: %v", err)
				}
			case "renamed requester", "old name reused":
				// sql-guarantee: no current account command renames an account;
				// sql-guarantee: model retained UUID identity across a name change and reuse.
				completionExec(t, a.DB, `UPDATE users SET username='alice-renamed' WHERE principal_id=$1`, captured.ID)
				// sql-guarantee: keep the principal's display name consistent
				// sql-guarantee: previously admitted authority retains its UUID.
				completionExec(t, a.DB, `UPDATE principals SET name='alice-renamed' WHERE id=$1`, captured.ID)
				if _, err := a.Access.EnsurePerson(t.Context(), "alice"); err != nil {
					t.Fatal(err)
				}
				if testPrincipalOf(t, a.DB, "alice") == captured.ID {
					t.Fatal("name reuse did not create a distinct person")
				}
				if scenario == "old name reused" {
					if changed, err := a.Access.Revoke(t.Context(), grant, "admin"); err != nil || !changed {
						t.Fatalf("withdraw original grant: %t %v", changed, err)
					}
					if _, err := a.Access.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "alice", Capability: string(access.CapDeploymentDeploy), Scope: access.ScopeProject, ScopeID: p.ID.String(), By: "admin"}, ""); err != nil {
						t.Fatal(err)
					}
				}
			case "missing snapshot", "malformed snapshot", "snapshot without UUID":
				raw := any(nil)
				if scenario == "malformed snapshot" {
					raw = `"not an authority"`
				}
				if scenario == "snapshot without UUID" {
					raw = `{"kind":"user","name":"alice","credential":"header:alice"}`
				}
				// sql-guarantee: represent installed legacy/corrupt request
				// sql-guarantee: current admission cannot create these snapshots.
				completionExec(t, a.DB, `UPDATE release_requests SET authority_json=$1::jsonb WHERE release_id=$2`, raw, accepted.ID)
			case "account disabled":
				disabled := true
				if err := a.Access.SaveUser(t.Context(), identity.AccountChange{Username: "alice", Disabled: &disabled}, admin); err != nil {
					t.Fatal(err)
				}
			}
			if !success && scenario != "missing snapshot" && scenario != "malformed snapshot" && scenario != "snapshot without UUID" {
				if err := a.Access.RequireStill(t.Context(), captured, permission); err == nil {
					t.Fatal("fixture did not withdraw authority")
				}
			}
			before := provider.Snapshot()
			err = a.Deployment.ExecuteRelease(context.Background(), accepted.ID)
			recorded, readErr := a.Deployment.Releases.ByNumber(t.Context(), p.ID, accepted.Number)
			if readErr != nil || recorded.ApprovedBy != "alice" {
				t.Fatalf("execution rewrote admitted attribution: %+v %v", recorded, readErr)
			}
			if success {
				if err != nil {
					t.Fatalf("current requester release: %v", err)
				}
				requireReleaseState(t, a, p.ID, accepted.Number, deployment.Released)
				if after := provider.Snapshot(); after.AppsMade <= before.AppsMade {
					t.Fatal("control did not create its application")
				}
				return
			}
			if err == nil {
				t.Error("withdrawn requester authority still executed release")
			}
			after := provider.Snapshot()
			for _, request := range after.Requests[len(before.Requests):] {
				if request.Method != "GET" && request.Method != "HEAD" {
					t.Errorf("withdrawn authority reached provider effect: %+v", request)
				}
			}
			if len(*provider.owned) != 0 {
				t.Errorf("withdrawn authority initialized managed database: %v", *provider.owned)
			}
			requireReleaseState(t, a, p.ID, accepted.Number, deployment.ReleaseFailed)
		})
	}
}

// Approval is a separate person's decision, bound to the release, checked
// again before managed database preparation, backups or application effects.
func TestReleaseApprovalRequiresCurrentIndependentHumanAssent(t *testing.T) {
	for _, scenario := range []string{"approved", "self", "agent", "wrong role", "no capability", "role revoked", "cancel requester", "cancel approver", "take down", "queued take down", "superseded"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			a, provider, p, bundle := doWorld(t)
			testAdministrator(t, a, "admin")
			admin := access.Principal{ID: testPrincipalOf(t, a.DB, "admin"), Kind: "user", Name: "admin", Via: "header", Credential: identity.HeaderCredential("admin")}
			for _, name := range []string{"requester", "manager", "outsider"} {
				if _, err := a.Access.EnsurePerson(t.Context(), name); err != nil {
					t.Fatal(err)
				}
			}
			person := func(name string) access.Principal {
				return access.Principal{ID: testPrincipalOf(t, a.DB, name), Kind: "user", Name: name, Via: "header", Credential: identity.HeaderCredential(name)}
			}
			grant := func(name, role string, cap access.Capability) {
				_, err := a.Access.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: name, Role: role, Capability: string(cap), Scope: access.ScopeProject, ScopeID: p.ID.String(), By: "admin"}, "")
				if err != nil {
					t.Fatal(err)
				}
			}
			grant("requester", access.RoleProjectMaintainer, "")
			grant("requester", access.RoleReleaseManager, "")
			roleID, err := a.Access.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "manager", Role: access.RoleReleaseManager, Scope: access.ScopeProject, ScopeID: p.ID.String(), By: "admin"}, "")
			if err != nil {
				t.Fatal(err)
			}
			grant("outsider", access.RoleProjectViewer, "")
			target, err := a.Deployment.Targets.ByName(t.Context(), "do-syd")
			if err != nil {
				t.Fatal(err)
			}
			dep, err := a.Deployment.Deployments.EnsureStanding(t.Context(), p.ID, "production", target.ID, deployment.Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"})
			if err != nil {
				t.Fatal(err)
			}
			feed := "manual"
			if scenario == "follows main" {
				feed = "main"
			}
			ctx := verifiedContext(t.Context(), person("requester"))
			dep, err = a.Deployment.SetFeed(ctx, p.ID, dep.Name, deployment.FeedSettings{Revision: dep.FeedRevision, Feed: feed, ApprovalRole: access.RoleReleaseManager})
			if err != nil {
				t.Fatal(err)
			}
			dep, err = a.Deployment.EditDeploymentLabel(verifiedContext(t.Context(), admin), p.ID, dep.ID, dep.DisplayLabel, "Production")
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := a.Deployment.RequestRelease(ctx, deployment.DeploySelection{ProjectID: p.ID, DeploymentName: dep.Name, BundleID: bundle.ID})
			if err != nil {
				t.Fatal(err)
			}
			if accepted.State.String() != "waiting for approval" {
				t.Fatalf("request ran without assent: %+v", accepted)
			}
			waitingRelease, err := a.Deployment.Releases.ByNumber(t.Context(), p.ID, accepted.Number)
			if err != nil {
				t.Fatal(err)
			}
			decision, err := a.Deployment.ReleaseDecision(verifiedContext(t.Context(), person("manager")), waitingRelease)
			if err != nil || decision.Meaning != fmt.Sprintf("I accept release %d running on Production", accepted.Number) {
				t.Fatalf("displayed meaning: %+v %v", decision, err)
			}
			if err := a.Deployment.ApproveRelease(verifiedContext(t.Context(), person("manager")), p.ID, accepted.Number, fmt.Sprintf("I accept release %d running on production", accepted.Number)); err == nil {
				t.Fatal("machine name accepted instead of displayed sentence")
			}
			events, err := a.Deployment.Releases.Events(t.Context(), accepted.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(events[len(events)-1].Text, decision.Meaning) {
				t.Fatalf("waiting event differs from displayed meaning: %+v", events)
			}
			before := provider.Snapshot()
			if err := a.Deployment.ExecuteRelease(context.Background(), accepted.ID); err != nil {
				t.Fatal(err)
			}
			requireReleaseWord(t, a, p.ID, accepted.Number, "waiting for approval")
			if after := provider.Snapshot(); len(after.Requests) != len(before.Requests) {
				t.Fatal("waiting request reached provider")
			}
			for _, name := range []string{"requester", "manager", "outsider"} {
				waiting, err := a.Deployment.WaitingReleases(verifiedContext(t.Context(), person(name)))
				if err != nil {
					t.Fatal(err)
				}
				if (len(waiting) == 1) != (name == "manager") {
					t.Fatalf("waiting tasks for %s: %+v", name, waiting)
				}
				viewer := verifiedContext(t.Context(), person(name))
				if name == "manager" {
					if _, err := a.Access.Require(viewer, person(name), access.Permission{Capability: access.CapProjectRead, Resource: access.Resource{Type: "project", ID: p.ID.String(), ProjectID: p.ID}}); err != nil {
						t.Fatalf("release manager cannot open project: %v", err)
					}
					releaseList := httptest.NewRecorder()
					read := httptest.NewRequest("GET", "/", nil)
					read.SetPathValue("slug", p.Slug)
					httpapi.NewReleaseHistory(a.Projects, a.Work, a.Deployment, a.backupRecords, a.DB).ListReleases(releaseList, read.WithContext(viewer))
					if releaseList.Code != 200 {
						t.Fatalf("release manager cannot read deployment history: %d %s", releaseList.Code, releaseList.Body.String())
					}
				}

				response := httptest.NewRecorder()
				httpapi.NewActivity(a.Work, a.Deployment).Activity(response, httptest.NewRequest("GET", "/", nil).WithContext(viewer))
				var activity httpapi.InstanceActivity
				if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &activity) != nil || (len(activity.ReleaseApprovals) == 1) != (name == "manager") {
					t.Fatalf("Home for %s: %d %s", name, response.Code, response.Body.String())
				}
				attention, err := attentionFor(a).Read(viewer)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, item := range attention.Items {
					if strings.HasPrefix(item.Key, "release-approval/") {
						found = true
						if item.Href != waiting[0].Href || item.Title != fmt.Sprintf("Release %d to Production needs your approval", accepted.Number) {
							t.Fatalf("approval task has wrong destination: %+v", item)
						}
					}
				}
				if found != (name == "manager") {
					t.Fatalf("Attention for %s: %+v", name, attention.Items)
				}

			}
			meaning := fmt.Sprintf("I accept release %d running on Production", accepted.Number)
			approver := person("manager")
			success := scenario == "approved"
			switch scenario {
			case "self":
				approver = person("requester")
			case "agent":
				approver.Via = "token"
				approver.Purpose = access.TokenAgent
				approver.Credential = "token:agent"
			case "wrong role":
				approver = admin
			case "no capability":
				// A named role alone is insufficient: choose a role that lacks release:approve.
				if _, err := a.Deployment.SetFeed(ctx, p.ID, dep.Name, deployment.FeedSettings{Revision: dep.FeedRevision, Feed: feed, ApprovalRole: access.RoleProjectViewer}); err != nil {
					t.Fatal(err)
				}
				// A fresh request captures this rule and supersedes the old request.
				accepted, err = a.Deployment.RequestRelease(ctx, deployment.DeploySelection{ProjectID: p.ID, DeploymentName: dep.Name, BundleID: bundle.ID})
				if err != nil {
					t.Fatal(err)
				}
				meaning = fmt.Sprintf("I accept release %d running on Production", accepted.Number)
				approver = person("outsider")
			case "cancel requester", "cancel approver":
				who := ctx
				if scenario == "cancel approver" {
					who = verifiedContext(t.Context(), approver)
				}
				if err := a.Deployment.CancelRelease(who, p.ID, accepted.Number); err != nil {
					t.Fatal(err)
				}
				requireReleaseWord(t, a, p.ID, accepted.Number, "cancelled")
				if err := a.Deployment.ApproveRelease(verifiedContext(t.Context(), approver), p.ID, accepted.Number, meaning); err == nil {
					t.Fatal("cancelled release approved")
				}
				return
			case "take down", "queued take down":
				if scenario == "queued take down" {
					before := provider.Snapshot()
					op, err := a.DataProtection.TakeDownForDeletion(t.Context(), p.ID, dep.Name, admin)
					if err != nil || op == nil {
						t.Fatalf("admit take-down: %+v %v", op, err)
					}
					requireReleaseWord(t, a, p.ID, accepted.Number, "cancelled")
					if after := provider.Snapshot(); len(after.Requests) != len(before.Requests) {
						t.Fatal("admission reached provider")
					}
					if _, err := a.operations.RunHere(t.Context(), op.ID); err != nil {
						t.Fatal(err)
					}
				} else if _, err := a.DataProtection.TakeDown(verifiedContext(t.Context(), admin), dataprotection.RemoveDeployment{ProjectID: p.ID, Name: dep.Name}); err != nil {
					t.Fatal(err)
				}
				requireReleaseWord(t, a, p.ID, accepted.Number, "cancelled")
				events, err := a.Deployment.Releases.Events(t.Context(), accepted.ID)
				if err != nil {
					t.Fatal(err)
				}
				want := fmt.Sprintf("cancelled: the deployment was taken down; removed by admin (principal %s) via header", admin.ID)
				if events[len(events)-1].Text != want {
					t.Fatalf("removal cancellation: %+v", events)
				}
				viewer := verifiedContext(t.Context(), person("manager"))
				response := httptest.NewRecorder()
				read := httptest.NewRequest("GET", "/", nil)
				read.SetPathValue("slug", p.Slug)
				httpapi.NewReleaseHistory(a.Projects, a.Work, a.Deployment, a.backupRecords, a.DB).ListReleases(response, read.WithContext(viewer))
				if response.Code != 200 || !strings.Contains(response.Body.String(), "cancelled") {
					t.Fatalf("history after removal: %d %s", response.Code, response.Body.String())
				}
				response = httptest.NewRecorder()
				httpapi.NewActivity(a.Work, a.Deployment).Activity(response, httptest.NewRequest("GET", "/", nil).WithContext(viewer))
				var activity httpapi.InstanceActivity
				if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &activity) != nil || len(activity.ReleaseApprovals) != 0 {
					t.Fatalf("Home after removal: %d %s", response.Code, response.Body.String())
				}
				attention, err := attentionFor(a).Read(viewer)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range attention.Items {
					if strings.HasPrefix(item.Key, "release-approval/") {
						t.Fatalf("approval task after removal: %+v", item)
					}
				}
				return
			case "superseded":
				newer, err := a.Deployment.RequestRelease(ctx, deployment.DeploySelection{ProjectID: p.ID, DeploymentName: dep.Name, BundleID: bundle.ID})
				if err != nil {
					t.Fatal(err)
				}
				old, err := a.Deployment.Releases.ByNumber(t.Context(), p.ID, accepted.Number)
				if err != nil || old.State.String() != "superseded" || old.SupersededBy != newer.ID {
					t.Fatalf("supersession: %+v %v", old, err)
				}
				if err := a.Deployment.ApproveRelease(verifiedContext(t.Context(), approver), p.ID, old.Number, meaning); err == nil {
					t.Fatal("superseded release approved")
				}
				return
			}
			// Exercise the public HTTP door as well as owner-only authority.
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(fmt.Sprintf(`{"meaning":%q}`, meaning)))
			req.SetPathValue("slug", p.Slug)
			req.SetPathValue("number", fmt.Sprint(accepted.Number))
			req = req.WithContext(verifiedContext(req.Context(), approver))
			response := httptest.NewRecorder()
			httpapi.NewReleaseOperations(a.Projects, a.Deployment, a.Preview, a.Work, a.DB, a.cfg.dataDir, nil).ApproveRelease(response, req)
			if !success && scenario != "role revoked" {
				if response.Code != http.StatusForbidden {
					t.Fatalf("ineligible approval: %d %s", response.Code, response.Body.String())
				}
				requireReleaseWord(t, a, p.ID, accepted.Number, "waiting for approval")
				return
			}
			if response.Code != http.StatusAccepted {
				t.Fatalf("approval: %d %s", response.Code, response.Body.String())
			}
			if scenario == "role revoked" {
				if _, err := a.Access.Revoke(t.Context(), roleID, "admin"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "approved" {
				if _, err := a.Deployment.EditDeploymentLabel(verifiedContext(t.Context(), admin), p.ID, dep.ID, "Production", "Customer service"); err != nil {
					t.Fatal(err)
				}
			}
			err = a.Deployment.ExecuteRelease(context.Background(), accepted.ID)
			recorded, readErr := a.Deployment.Releases.ByNumber(t.Context(), p.ID, accepted.Number)
			if readErr != nil || recorded.Approval == nil || recorded.Approval.Actor.Principal != approver.ID || recorded.Approval.Actor.Via != "header" || recorded.Approval.Meaning != meaning || recorded.Approval.At.IsZero() {
				t.Fatalf("assent record: %+v %v", recorded, readErr)
			}
			if success {
				if err != nil {
					t.Fatal(err)
				}
				requireReleaseState(t, a, p.ID, accepted.Number, deployment.Released)

				// An unauthorized caller gets no decision-state details after settlement.
				operations := httpapi.NewReleaseOperations(a.Projects, a.Deployment, a.Preview, a.Work, a.DB, a.cfg.dataDir, nil)
				for _, act := range []func(http.ResponseWriter, *http.Request){operations.ApproveRelease, operations.CancelRelease} {
					request := httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf(`{"meaning":%q}`, meaning)))
					request.SetPathValue("slug", p.Slug)
					request.SetPathValue("number", fmt.Sprint(accepted.Number))
					response := httptest.NewRecorder()
					act(response, request.WithContext(verifiedContext(t.Context(), person("outsider"))))
					if response.Code != http.StatusForbidden {
						t.Fatalf("unauthorized settled-release decision: %d %s", response.Code, response.Body.String())
					}
				}
			} else {
				if err == nil {
					t.Fatal("revoked approver ran release")
				}
				requireReleaseState(t, a, p.ID, accepted.Number, deployment.ReleaseFailed)
				after := provider.Snapshot()
				for _, r := range after.Requests[len(before.Requests):] {
					if r.Method != "GET" && r.Method != "HEAD" {
						t.Fatalf("revoked assent reached effects: %+v", r)
					}
				}
				if len(*provider.owned) != 0 {
					t.Fatal("revoked assent initialized database")
				}
			}
		})
	}
}

func requireReleaseWord(t *testing.T, a *App, projectID ids.ID, number int64, state string) {
	t.Helper()
	rel, err := a.Deployment.Releases.ByNumber(t.Context(), projectID, number)
	if err != nil || rel.State.String() != state {
		t.Fatalf("release %d: %+v %v; want %s", number, rel, err, state)
	}
}
