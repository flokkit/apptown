package deployment

import (
	"errors"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/fault"
)

func promotionFeedFixture(t *testing.T) (*digestWorld, *Deployment, *ReleaseBundle) {
	t.Helper()
	w := newDigestWorld(t, "", nil)
	b := w.publish(t)
	if _, err := w.promote(t, b.ID, "staging", w.lab); err != nil {
		t.Fatal(err)
	}
	dep, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "production", w.lab2.ID, Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	dep, err = w.s.SetFeed(t.Context(), w.project, dep.Name, FeedSettings{Revision: dep.FeedRevision, Feed: "promotion", From: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	return w, dep, b
}

// The source's applied bundle wins over a newer bundle, and remains pinned if
// the source changes after the request. The release keeps the source identity.
func TestPromotionDeploysSourceRunningBundleAndRecordsSource(t *testing.T) {
	t.Parallel()
	w, dep, running := promotionFeedFixture(t)
	if err := w.s.ObserveRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	origin := running.BuiltFrom
	origin.By = &claimedAttempt{db: w.db, project: w.project, run: origin.RunID, step: origin.StepID, at: origin.Attempt + 1}
	newer, err := w.s.Bundles.Cut(t.Context(), w.project, w.revision, running.Parts, running.Init, w.contract, origin)
	if err != nil {
		t.Fatal(err)
	}
	if newer == running.ID {
		t.Fatal("fixture needs a newer bundle")
	}
	accepted, err := w.s.RequestRelease(t.Context(), DeploySelection{ProjectID: w.project, DeploymentName: dep.Name})
	if err != nil {
		t.Fatal(err)
	}
	source, err := w.s.Deployments.ByName(t.Context(), w.project, "staging")
	if err != nil {
		t.Fatal(err)
	}
	// Record a later source generation through its owner; the accepted release
	// must still execute the bundle chosen when the person requested promotion.
	at, _, err := w.s.Outcomes.Admit(t.Context(), Start{DeploymentID: source.ID, TargetID: source.TargetID, Key: "newer-source", Inputs: Inputs{Revision: w.revision, BundleID: newer}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Outcomes.Complete(t.Context(), Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", Health: "healthy"}}); err != nil {
		t.Fatal(err)
	}
	if err := w.s.ExecuteRelease(t.Context(), accepted.ID); err != nil {
		t.Fatal(err)
	}
	// Read the persisted ledger independently of the request response.
	got, err := w.s.Releases.ByNumber(t.Context(), w.project, accepted.Number)
	if err != nil || got.State != Released || got.BundleID != running.ID || got.FromDeploymentID != source.ID {
		t.Fatalf("promotion: %+v %v", got, err)
	}
}

// A numbered web action binds the displayed bundle; a later successful source
// release must require another decision, while a matching display ships exactly it.
func TestPromotionHonorsDisplayedBundle(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale=%t", stale), func(t *testing.T) {
			w, dep, running := promotionFeedFixture(t)
			summaries, err := w.s.List(t.Context(), w.project)
			if err != nil {
				t.Fatal(err)
			}
			in := DeploySelection{ProjectID: w.project, DeploymentName: dep.Name}
			for _, summary := range summaries {
				if summary.ID == dep.ID {
					in.ExpectedSourceBundleID = summary.PromotionBundleID
					if summary.PromotionRelease == 0 || in.ExpectedSourceBundleID != running.ID {
						t.Fatalf("display: %+v", summary)
					}
				}
			}
			// A newer project bundle alone must not change what is promoted.
			origin := running.BuiltFrom
			origin.By = &claimedAttempt{db: w.db, project: w.project, run: origin.RunID, step: origin.StepID, at: origin.Attempt + 1}
			newer, err := w.s.Bundles.Cut(t.Context(), w.project, w.revision, running.Parts, running.Init, w.contract, origin)
			if err != nil {
				t.Fatal(err)
			}
			var current *Release
			if stale {
				current, err = w.promote(t, newer, "staging", w.lab)
				if err != nil {
					t.Fatal(err)
				}
			}
			accepted, err := w.s.RequestRelease(t.Context(), in)
			if stale {
				want := fmt.Sprintf("staging now runs release %d; promote again", current.Number)
				if err == nil || err.Error() != want || fault.KindOf(err) != fault.Conflict || !accepted.ID.IsZero() {
					t.Fatalf("stale promotion: %+v %v; want %s", accepted, err, want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := w.s.ExecuteRelease(t.Context(), accepted.ID); err != nil {
				t.Fatal(err)
			}
			got, err := w.s.Releases.ByNumber(t.Context(), w.project, accepted.Number)
			if err != nil || got.State != Released || got.BundleID != running.ID {
				t.Fatalf("matching promotion: %+v %v", got, err)
			}
		})
	}
}

func TestPromotionRefusesEmptySource(t *testing.T) {
	t.Parallel()
	w, dep, _ := promotionFeedFixture(t)
	empty, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "empty", w.lab.ID, Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	dep, err = w.s.SetFeed(t.Context(), w.project, dep.Name, FeedSettings{Revision: dep.FeedRevision, Feed: "promotion", From: empty.Name})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.RequestRelease(t.Context(), DeploySelection{ProjectID: w.project, DeploymentName: dep.Name}); !errors.Is(err, errPromotionEmpty) {
		t.Fatalf("empty source reason: %v", err)
	}
	summaries, err := w.s.List(t.Context(), w.project)
	if err != nil {
		t.Fatal(err)
	}
	for _, summary := range summaries {
		if summary.ID == dep.ID && summary.RuntimeControls["deploy"] != errPromotionEmpty.Error() {
			t.Fatalf("disabled reason: %+v", summary)
		}
	}
}

func TestFeedSettingsRefuseCyclesStaleAndForeignSourcesAndRecordMainSetter(t *testing.T) {
	t.Parallel()
	s, in, _, _ := requestFixture(t)
	dep, err := s.Deployments.ByName(t.Context(), in.ProjectID, in.DeploymentName)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Deployments.EnsureStanding(t.Context(), in.ProjectID, "other", dep.TargetID, Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	first := FeedSettings{Revision: dep.FeedRevision, Feed: "promotion", From: other.Name}
	dep, err = s.SetFeed(t.Context(), in.ProjectID, dep.Name, first)
	if err != nil {
		t.Fatal(err)
	}
	for _, from := range []string{dep.Name, "absent"} {
		if _, err := s.SetFeed(t.Context(), in.ProjectID, other.Name, FeedSettings{Revision: other.FeedRevision, Feed: "promotion", From: from}); err == nil {
			t.Fatalf("accepted cyclic or foreign source %s", from)
		}
	}
	if _, err := s.SetFeed(t.Context(), in.ProjectID, dep.Name, first); !errors.Is(err, errFeedMoved) {
		t.Fatalf("stale: %v", err)
	}
	confirmed, err := s.SetFeed(t.Context(), in.ProjectID, dep.Name, FeedSettings{Revision: dep.FeedRevision, Feed: "main"})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := releasePrincipal(confirmed.feedAuthority)
	if err != nil || principal.Name != "alice" || principal.Credential != "header:alice" || !confirmed.PromotedFromID.IsZero() || confirmed.FeedRevision != dep.FeedRevision+1 {
		t.Fatalf("setter: %+v %+v %v", confirmed, principal, err)
	}
}

func TestPromotionRechecksDeployAuthorityAtEffectForAnApproverOnlyPerson(t *testing.T) {
	t.Parallel()
	w, dep, _ := promotionFeedFixture(t)
	people := identity.New(w.db, identity.Config{Projects: project.NewCatalogue(w.db)})
	if _, err := people.EnsurePerson(t.Context(), "owner"); err != nil {
		t.Fatal(err)
	}
	grantReal(t, people, "owner", access.RoleProjectApprover, "", access.ScopeProject, w.project.String())
	grant, err := people.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "owner", Capability: string(access.CapDeploymentDeploy), Scope: access.ScopeProject, ScopeID: w.project.String(), By: "fixture"}, "")
	if err != nil {
		t.Fatal(err)
	}
	w.s.release.Authority = access.Checked{Authority: people, Projects: project.New(w.db, nil)}
	w.s.release.Still = people
	actor, err := identity.ActorIn(t.Context(), w.db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	caller := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: actor.Principal, Kind: "user", Name: "owner", Via: "header", Credential: identity.HeaderCredential("owner")}, Verified: true})
	accepted, err := w.s.RequestRelease(caller, DeploySelection{ProjectID: w.project, DeploymentName: dep.Name})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.db.InTx(t.Context(), func(tx *database.Tx) error {
		_, err := identity.RevokeIn(t.Context(), tx, grant, "fixture", w.s.now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.RequestRelease(caller, DeploySelection{ProjectID: w.project, DeploymentName: dep.Name}); err == nil {
		t.Fatal("approver requested promotion without deploy authority")
	}
	if err := w.s.ExecuteRelease(t.Context(), accepted.ID); err == nil {
		t.Fatal("approver-only authority executed promotion")
	} else {
		var denied *access.Denied
		if !errors.As(err, &denied) || denied.Code != access.DenyMissingCapability {
			t.Fatalf("expected deployment permission refusal, got %v", err)
		}
	}
	current, err := w.s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil || current.DesiredGeneration != 0 {
		t.Fatalf("unauthorized effect: %+v %v", current, err)
	}
	rel, err := w.s.Releases.ByNumber(t.Context(), w.project, accepted.Number)
	if err != nil || rel.State != ReleaseFailed {
		t.Fatalf("refusal not recorded: %+v %v", rel, err)
	}
}

func TestProtectedPromotionNeedsDeployAndPromoteAtExecution(t *testing.T) {
	t.Parallel()
	w, dep, _ := promotionFeedFixture(t)
	if _, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, dep.Name, dep.TargetID, Attributes{Feed: "promotion", Audience: "public", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	auth := &policyAuthority{}
	w.s.release.Authority, w.s.release.Still = auth, auth
	for _, capability := range []access.Capability{access.CapDeploymentDeploy, access.CapReleasePromote} {
		auth.denied = capability
		if _, err := w.s.RequestRelease(t.Context(), DeploySelection{ProjectID: w.project, DeploymentName: dep.Name}); !errors.Is(err, access.ErrAuthorityUnavailable) {
			t.Fatalf("missing %s: %v", capability, err)
		}
	}
	auth.denied = ""
	accepted, err := w.s.RequestRelease(t.Context(), DeploySelection{ProjectID: w.project, DeploymentName: dep.Name})
	if err != nil {
		t.Fatal(err)
	}
	auth.denied = access.CapReleasePromote
	if err := w.s.ExecuteRelease(t.Context(), accepted.ID); !errors.Is(err, access.ErrAuthorityUnavailable) {
		t.Fatalf("revoked protected promotion: %v", err)
	}
	current, err := w.s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil || current.DesiredGeneration != 0 {
		t.Fatalf("protected effect: %+v %v", current, err)
	}
}

// A displayed promotion cannot become a manual release after a settings edit,
// whether the edit happens before selection or during contract resolution.
func TestPromotionExpectationSurvivesFeedChange(t *testing.T) {
	for _, atAdmission := range []bool{false, true} {
		t.Run(fmt.Sprintf("atAdmission=%t", atAdmission), func(t *testing.T) {
			t.Parallel()
			w, dep, displayed := promotionFeedFixture(t)
			summaries, err := w.s.List(t.Context(), w.project)
			if err != nil {
				t.Fatal(err)
			}
			in := DeploySelection{ProjectID: w.project, DeploymentName: dep.Name}
			for _, summary := range summaries {
				if summary.ID == dep.ID {
					in.ExpectedSourceBundleID = summary.PromotionBundleID
				}
			}
			if in.ExpectedSourceBundleID != displayed.ID {
				t.Fatal("missing displayed bundle")
			}
			origin := displayed.BuiltFrom
			origin.By = &claimedAttempt{db: w.db, project: w.project, run: origin.RunID, step: origin.StepID, at: origin.Attempt + 1}
			newer, err := w.s.Bundles.Cut(t.Context(), w.project, w.revision, displayed.Parts, displayed.Init, w.contract, origin)
			if err != nil {
				t.Fatal(err)
			}
			if newer == displayed.ID {
				t.Fatal("fixture needs a newer bundle")
			}
			before, err := w.s.Releases.ForProject(t.Context(), w.project, 100)
			if err != nil {
				t.Fatal(err)
			}
			changeFeed := func() {
				if _, err := w.s.SetFeed(t.Context(), w.project, dep.Name, FeedSettings{Revision: dep.FeedRevision, Feed: "manual"}); err != nil {
					t.Fatal(err)
				}
			}
			if atAdmission {
				w.s.release.Sources = &requestSources{before: changeFeed}
			} else {
				changeFeed()
			}
			accepted, err := w.s.RequestRelease(t.Context(), in)
			want := dep.Name + " is no longer promoted from another deployment; reload"
			if err == nil || fault.KindOf(err) != fault.Conflict || err.Error() != want || !accepted.ID.IsZero() {
				t.Fatalf("stale promotion: %+v %v; want conflict %q", accepted, err, want)
			}
			after, err := w.s.Releases.ForProject(t.Context(), w.project, 100)
			if err != nil || len(after) != len(before) {
				t.Fatalf("refusal created a release: %+v %v", after, err)
			}
			current, err := w.s.Deployments.ByID(t.Context(), dep.ID)
			if err != nil || current.DesiredGeneration != dep.DesiredGeneration {
				t.Fatalf("refusal deployed: %+v %v", current, err)
			}
		})
	}
}
