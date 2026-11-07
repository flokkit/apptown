package deployment

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Choosing earlier code must select immutable bytes, independent of the feed's
// newest bundle, and persist its rollback attribution on the same release path.
func TestChosenReleaseDeploysItsBundleAndRecordsRollback(t *testing.T) {
	t.Parallel()
	w, dep, earlier := promotionFeedFixture(t)
	first, err := w.s.Releases.LastReleasedForDeployment(t.Context(), chosenDeployment(t, w.s, w.project, "staging").ID)
	if err != nil {
		t.Fatal(err)
	}
	origin := earlier.BuiltFrom
	origin.By = &claimedAttempt{db: w.db, project: w.project, run: origin.RunID, step: origin.StepID, at: origin.Attempt + 1}
	newer, err := w.s.Bundles.Cut(t.Context(), w.project, w.revision, earlier.Parts, earlier.Init, w.contract, origin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.promote(t, newer, "staging", w.lab); err != nil {
		t.Fatal(err)
	}
	backup := &backupBeforeRelease{complete: true}
	w.s.release.Data = backup
	admitted, err := w.s.RequestRelease(t.Context(), DeploySelection{ProjectID: w.project, DeploymentName: "staging", ReleaseNumber: first.Number})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.ExecuteRelease(t.Context(), admitted.ID); err != nil {
		t.Fatal(err)
	}
	got, err := w.s.Releases.ByID(t.Context(), admitted.ID)
	if err != nil || got.State != Released || got.BundleID != earlier.ID || got.RollbackOf != first.ID || strings.Join(backup.calls, ",") != "before,scheduled backup,continuity" {
		t.Fatalf("rollback: %+v %v calls=%v", got, err, backup.calls)
	}
	// Another deployment is unchanged by the selection.
	other, err := w.s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil || other.DesiredGeneration != dep.DesiredGeneration {
		t.Fatalf("another deployment changed: %+v %v", other, err)
	}
}

func chosenDeployment(t *testing.T, s *Service, project ids.ID, name string) *Deployment {
	t.Helper()
	d, err := s.Deployments.ByName(t.Context(), project, name)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

type rollbackRestore struct {
	number  int64
	calls   int
	restore func()
}

func (r *rollbackRestore) ReleaseBefore(context.Context, ids.ID, string, ids.ID) (int64, error) {
	return r.number, nil
}
func (r *rollbackRestore) RestoreForRelease(context.Context, ids.ID, string, ids.ID, ids.ID, access.Principal) error {
	r.calls++
	if r.restore != nil {
		r.restore()
	}
	return nil
}

// Preparation after restoration may fail. Never resume rejected code as
// cleanup, and keep the stopped-data outcome in the persisted release log.
func TestPairedRollbackChecksAuthorityAndApprovalBeforeRestoreAndReportsStoppedFailure(t *testing.T) {
	for _, scenario := range []string{"waiting", "authority revoked", "pre-start failure", "success"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			w, _, _ := promotionFeedFixture(t)
			dep := chosenDeployment(t, w.s, w.project, "staging")
			first, err := w.s.Releases.LastReleasedForDeployment(t.Context(), dep.ID)
			if err != nil {
				t.Fatal(err)
			}
			// The fake backup collaborator's persisted reference stands for the
			// data owner's copy; its separate tests exercise the actual restore mode.
			if _, err := deploymentsql.New(w.db.SQL()).AttachReleaseBackup(t.Context(), deploymentsql.AttachReleaseBackupParams{ID: first.ID, BackupID: idstest.N(991)}); err != nil {
				t.Fatal(err)
			}
			if scenario == "waiting" {
				if _, err := w.s.SetFeed(t.Context(), w.project, "staging", FeedSettings{Revision: dep.FeedRevision, Feed: "manual", ApprovalRole: "release-manager"}); err != nil {
					t.Fatal(err)
				}
			}
			r := &rollbackRestore{number: first.Number}
			w.s.release.Rollbacks = r
			copy := &backupBeforeRelease{complete: scenario != "pre-start failure"}
			w.s.release.Data = copy
			admitted, err := w.s.RequestRelease(t.Context(), DeploySelection{ProjectID: w.project, DeploymentName: "staging", BeforeRelease: first.Number})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "authority revoked" {
				auth := &policyAuthority{denied: access.CapDeploymentDeploy}
				w.s.release.Still = auth
			}
			err = w.s.ExecuteRelease(t.Context(), admitted.ID)
			if scenario == "waiting" || scenario == "authority revoked" {
				if r.calls != 0 || len(copy.calls) != 0 {
					t.Fatalf("effects preceded approval/authority: %d %v", r.calls, copy.calls)
				}
				return
			}
			if r.calls != 1 {
				t.Fatalf("restore count %d", r.calls)
			}
			got, readErr := w.s.Releases.ByID(t.Context(), admitted.ID)
			if readErr != nil || got.RestoreBackupID != idstest.N(991) || got.RollbackOf != first.ID {
				t.Fatalf("pair record: %+v %v", got, readErr)
			}
			if scenario == "pre-start failure" {
				events, e := w.s.Releases.Events(t.Context(), admitted.ID)
				if err == nil || e != nil || got.State != ReleaseFailed || !strings.Contains(events[len(events)-1].Text, "restored data stays in place and the app remains stopped") {
					t.Fatalf("stopped failure: %+v %v %v", got, err, events)
				}
			} else if err != nil || got.State != Released {
				t.Fatalf("pair: %+v %v", got, err)
			}
		})
	}
}

func TestChosenReleaseRefusesMixedSelections(t *testing.T) {
	t.Parallel()
	s, in, _, _ := requestFixture(t)
	in.ReleaseNumber = 1
	if _, err := s.RequestRelease(t.Context(), in); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("mixed choice accepted: %v", err)
	}
}

func TestChosenBundleOverridesPromotionFeedWithoutChangingItsSource(t *testing.T) {
	t.Parallel()
	w, dep, sourceBundle := promotionFeedFixture(t)
	origin := sourceBundle.BuiltFrom
	origin.By = &claimedAttempt{db: w.db, project: w.project, run: origin.RunID, step: origin.StepID, at: origin.Attempt + 1}
	chosen, err := w.s.Bundles.Cut(t.Context(), w.project, w.revision, sourceBundle.Parts, sourceBundle.Init, w.contract, origin)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := w.s.RequestRelease(t.Context(), DeploySelection{ProjectID: w.project, DeploymentName: dep.Name, BundleID: chosen})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.ExecuteRelease(t.Context(), accepted.ID); err != nil {
		t.Fatal(err)
	}
	recorded, err := w.s.Releases.ByID(t.Context(), accepted.ID)
	if err != nil || recorded.BundleID != chosen || !recorded.FromDeploymentID.IsZero() {
		t.Fatalf("chosen promotion-feed code: %+v %v", recorded, err)
	}
	source, err := w.s.Releases.LastReleasedForDeployment(t.Context(), dep.PromotedFromID)
	if err != nil || source.BundleID != sourceBundle.ID {
		t.Fatalf("source changed: %+v %v", source, err)
	}
}

// A retry is a separately authorized, numbered attempt of the pinned bundle.
// The failed receipt stays failed, including when a newer bundle has landed.
func TestFailedReleaseCanBeRetriedWithCurrentAuthorityAndApproval(t *testing.T) {
	for _, selection := range []string{"number", "follows main latest"} {
		for _, gate := range []string{"allowed", "denied", "approval"} {
			t.Run(selection+"/"+gate, func(t *testing.T) {
				t.Parallel()
				s, in, authority, _ := requestFixture(t)
				dep := chosenDeployment(t, s, in.ProjectID, in.DeploymentName)
				original, err := s.RequestRelease(t.Context(), in)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.Outcomes.FailUnstartedRelease(t.Context(), original.ID, errors.New("provider refused")); err != nil {
					t.Fatal(err)
				}
				cutBundleFixture(t, s.db, in.ProjectID, "newer", BundleOrigin{})
				attrs := dep.releaseAttributes()
				if selection == "follows main latest" {
					attrs.Feed = "main"
				}
				if _, err := s.Deployments.EnsureStanding(t.Context(), in.ProjectID, dep.Name, dep.TargetID, attrs); err != nil {
					t.Fatal(err)
				}
				if gate == "approval" {
					// The rule changes after the failed release; the retry needs fresh assent.
					current := chosenDeployment(t, s, in.ProjectID, dep.Name)
					s.configuration.Authority = authority
					if _, err := s.SetFeed(t.Context(), in.ProjectID, dep.Name, FeedSettings{Revision: current.FeedRevision, Feed: attrs.Feed, ApprovalRole: "release-manager"}); err != nil {
						t.Fatal(err)
					}
				}
				if gate == "denied" {
					authority.deny = errors.New("deploy permission withdrawn")
				}
				retry := DeploySelection{ProjectID: in.ProjectID, DeploymentName: dep.Name}
				if selection == "number" {
					retry.ReleaseNumber = original.Number
				}
				accepted, err := s.RequestRelease(t.Context(), retry)
				if gate == "denied" {
					if !errors.Is(err, authority.deny) {
						t.Fatalf("retry bypassed authority: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				got, err := s.Releases.ByID(t.Context(), accepted.ID)
				if err != nil || got.BundleID != in.BundleID || got.ID == original.ID || got.RollbackOf != original.ID {
					t.Fatalf("retry lost provenance: %+v %v", got, err)
				}
				if gate == "approval" && got.State != releaseWaiting {
					t.Fatalf("retry bypassed approval: %s", got.State)
				}
				failed, err := s.Releases.ByID(t.Context(), original.ID)
				if err != nil || failed.State != ReleaseFailed {
					t.Fatalf("failed history rewritten: %+v %v", failed, err)
				}
			})
		}
	}
}
