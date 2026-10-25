package apptown

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/access"
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
			if _, err := a.Access.EnsureUser(t.Context(), "alice"); err != nil {
				t.Fatal(err)
			}
			target, err := a.Deployment.Targets.ByName(t.Context(), "do-syd")
			if err != nil {
				t.Fatal(err)
			}
			dep, err := a.Deployment.Deployments.EnsureStanding(t.Context(), p.ID, "cloud", target.ID, deployment.Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"})
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
				completionExec(t, a.DB, `UPDATE users SET username='alice-renamed' WHERE principal_id=?`, captured.ID)
				// sql-guarantee: keep the principal's display name consistent
				// sql-guarantee: previously admitted authority retains its UUID.
				completionExec(t, a.DB, `UPDATE principals SET name='alice-renamed' WHERE id=?`, captured.ID)
				if _, err := a.Access.EnsureUser(t.Context(), "alice"); err != nil {
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
				completionExec(t, a.DB, `UPDATE release_requests SET authority_json=?::jsonb WHERE release_id=?`, raw, accepted.ID)
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
