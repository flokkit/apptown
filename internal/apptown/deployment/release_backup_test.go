package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

type backupBeforeRelease struct {
	calls         []string
	complete      bool
	afterProblems []string
}

func (b *backupBeforeRelease) BeforeRelease(context.Context, ids.ID, string, bool) (ReleaseWitness, error) {
	b.calls = append(b.calls, "before")
	return backupReleaseWitness{b}, nil
}
func (b *backupBeforeRelease) AfterRelease(ids.ID, string) {
	b.calls = append(b.calls, "scheduled backup")
}

type backupReleaseWitness struct{ b *backupBeforeRelease }

func (w backupReleaseWitness) Complete() bool { return w.b.complete }
func (w backupReleaseWitness) CopyEvidence() json.RawMessage {
	return json.RawMessage(`{"outcome":"test-copy","reason":"copy evidence retained"}`)
}
func (w backupReleaseWitness) BackupID() ids.ID  { return ids.Nil }
func (w backupReleaseWitness) CopyWords() string { return "copy evidence retained" }
func (w backupReleaseWitness) After(context.Context, []string) ([]string, []string) {
	w.b.calls = append(w.b.calls, "continuity")
	return w.b.afterProblems, []string{"not comparable"}
}

func TestEveryReleaseTakesBackupAndKeepsContinuitySeparate(t *testing.T) {
	for _, scenario := range []string{"backup fails", "success", "continuity fails"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			p := newPromotionTargets(t, false)
			p.publish(t)
			selected := p.chooseDefault(t)
			backup := &backupBeforeRelease{complete: scenario != "backup fails"}
			if scenario == "continuity fails" {
				backup.afterProblems = []string{"data changed"}
			}
			p.s.release.Data = backup
			accepted, err := p.s.RequestDefaultRelease(t.Context(), DefaultReleaseSelection{ProjectID: p.project, ExpectedRevision: &selected.Revision})
			if err != nil {
				t.Fatal(err)
			}
			err = p.s.ExecuteRelease(t.Context(), accepted.ID)
			rel, readErr := p.s.Releases.ByNumber(t.Context(), p.project, accepted.Number)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if scenario == "backup fails" {
				if err == nil || rel.State != ReleaseFailed || rel.FailedStage != deploy.BackingUp || len(backup.calls) != 1 {
					t.Fatalf("backup failure did not stop release: %+v %v %v", rel, backup.calls, err)
				}
				if _, n := p.lastStack(t); n != 0 {
					t.Fatal("backup failure reached provider")
				}
			} else {
				if rel.State != Released || rel.Stage != deploy.Live || strings.Join(backup.calls, ",") != "before,scheduled backup,continuity" {
					t.Fatalf("release ordering: %+v %v %v", rel, backup.calls, err)
				}
				if (err != nil) != (scenario == "continuity fails") {
					t.Fatalf("continuity: %v", err)
				}
				if scenario == "continuity fails" && !strings.Contains(err.Error(), "release is running") {
					t.Fatal(err)
				}
			}
			metadata, e := NewReleases(p.s.db).RequestMetadata(t.Context(), accepted.ID)
			if e != nil || metadata == nil || !strings.Contains(string(metadata.BackupEvidence), "copy evidence retained") {
				t.Fatalf("backup evidence: %+v %v", metadata, e)
			}

			if errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

// A manual exception is scoped to the person's known copy problem. It cannot
// authorize a different failure discovered after admission.
func TestManualUncopiedAcceptanceIsRecordedAndRechecked(t *testing.T) {
	for _, scenario := range []string{"accepted", "copy problem changed", "grant revoked"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			p := newPromotionTargets(t, false)
			p.publish(t)
			selected := p.chooseDefault(t)
			p.s.copyCheck = copyCheckSays("async edge environment")
			p.s.release.Data = &backupBeforeRelease{}
			accepted, err := p.s.RequestDefaultRelease(t.Context(), DefaultReleaseSelection{ProjectID: p.project, ExpectedRevision: &selected.Revision, AcceptUncopiedData: true})
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := p.s.Releases.RequestMetadata(t.Context(), accepted.ID)
			if err != nil || metadata == nil || !strings.Contains(string(metadata.UncopiedAcceptance), "async edge environment") || !strings.Contains(string(metadata.UncopiedAcceptance), "alice") {
				t.Fatalf("acceptance missing: %+v %v", metadata, err)
			}
			if scenario == "copy problem changed" {
				p.s.copyCheck = copyCheckSays("backup store unavailable")
			}
			if scenario == "grant revoked" {
				p.s.release.Still.(*requestAuthority).deny = &access.Denied{Reason: "deploy grant revoked"}
			}
			err = p.s.ExecuteRelease(t.Context(), accepted.ID)
			rel, readErr := p.s.Releases.ByNumber(t.Context(), p.project, accepted.Number)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if scenario == "accepted" {
				if err != nil || rel.State != Released {
					t.Fatalf("accepted release: %+v %v", rel, err)
				}
			} else if err == nil || rel.State != ReleaseFailed {
				t.Fatalf("execution bypassed recheck: %+v %v", rel, err)
			}
		})
	}
}

func TestAutomaticReleaseNeverAcceptsUncopiedData(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	bundle := p.publish(t)
	dep, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, "follows", p.lab.ID, Attributes{Feed: "main", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.s.ConfirmFeed(t.Context(), p.project, dep.Name, dep.FeedRevision); err != nil {
		t.Fatal(err)
	}
	p.s.copyCheck = copyCheckSays("uncopyable data")
	p.s.release.Data = &backupBeforeRelease{}
	if err = p.s.RequestLandingReleases(t.Context(), p.project, bundle.BuiltFrom.RunID, bundle.BuiltFrom.ChangeID, "approver", false); err != nil {
		t.Fatal(err)
	}
	requests, err := p.s.Releases.ForProject(t.Context(), p.project, 50)
	if err != nil || len(requests) != 1 {
		t.Fatalf("requests: %+v %v", requests, err)
	}
	if err = p.s.ExecuteRelease(t.Context(), requests[0].ID); err == nil {
		t.Fatal("automatic release accepted incomplete copy")
	}
	rel, err := p.s.Releases.ByNumber(t.Context(), p.project, requests[0].Number)
	if err != nil || rel.State != ReleaseFailed {
		t.Fatalf("release: %+v %v", rel, err)
	}
	metadata, err := p.s.Releases.RequestMetadata(t.Context(), rel.ID)
	if err != nil || metadata == nil || len(metadata.UncopiedAcceptance) != 0 || len(metadata.BackupEvidence) == 0 {
		t.Fatalf("evidence: %+v %v", metadata, err)
	}
}

// Promotion keeps the source bundle while approval waits. Revoking the role
// after assent stops execution before even the pre-release backup is taken.
func TestPromotionApprovalRechecksRoleBeforeBackup(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(fmt.Sprint(revoked), func(t *testing.T) {
			t.Parallel()
			w, dep, bundle := promotionFeedFixture(t)
			accounts := identity.New(w.db, identity.Config{Projects: project.NewCatalogue(w.db)})
			if err := accounts.SeedAdministrator(t.Context(), "alice"); err != nil {
				t.Fatal(err)
			}
			if _, err := accounts.EnsurePerson(t.Context(), "manager"); err != nil {
				t.Fatal(err)
			}
			role, err := accounts.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "manager", Role: access.RoleReleaseManager, Scope: access.ScopeProject, ScopeID: w.project.String(), By: "fixture"}, "")
			if err != nil {
				t.Fatal(err)
			}
			w.s.release.Authority = access.Checked{Authority: accounts, Projects: project.New(w.db, nil)}
			w.s.release.Still = accounts
			w.s.release.Grants = accounts
			caller := func(name string) context.Context {
				actor, err := identity.ActorIn(t.Context(), w.db, name)
				if err != nil {
					t.Fatal(err)
				}
				return access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{ID: actor.Principal, Kind: "user", Name: name, Via: "header", Credential: identity.HeaderCredential(name)}})
			}
			dep, err = w.s.SetFeed(caller("alice"), w.project, dep.Name, FeedSettings{Revision: dep.FeedRevision, Feed: "promotion", From: "staging", ApprovalRole: access.RoleReleaseManager})
			if err != nil {
				t.Fatal(err)
			}
			backup := &backupBeforeRelease{complete: true}
			w.s.release.Data = backup
			accepted, err := w.s.RequestRelease(caller("alice"), DeploySelection{ProjectID: w.project, DeploymentName: dep.Name})
			if err != nil {
				t.Fatal(err)
			}
			if accepted.State != releaseWaiting {
				t.Fatalf("promotion bypassed approval: %+v", accepted)
			}
			if err := w.s.ExecuteRelease(t.Context(), accepted.ID); err != nil || len(backup.calls) != 0 {
				t.Fatalf("waiting release took backup: %v %v", err, backup.calls)
			}
			meaning := fmt.Sprintf("I accept release %d running on production", accepted.Number)
			if err := w.s.ApproveRelease(caller("manager"), w.project, accepted.Number, meaning); err != nil {
				t.Fatal(err)
			}
			if revoked {
				if _, err := accounts.Revoke(t.Context(), role, "fixture"); err != nil {
					t.Fatal(err)
				}
			}
			err = w.s.ExecuteRelease(t.Context(), accepted.ID)
			rel, readErr := w.s.Releases.ByNumber(t.Context(), w.project, accepted.Number)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if revoked {
				if err == nil || rel.State != ReleaseFailed || len(backup.calls) != 0 {
					t.Fatalf("revoked approver reached backup: %+v %v %v", rel, err, backup.calls)
				}
			} else if err != nil || rel.State != Released || rel.BundleID != bundle.ID || len(backup.calls) == 0 {
				t.Fatalf("approved promotion: %+v %v %v", rel, err, backup.calls)
			}
		})
	}
}
