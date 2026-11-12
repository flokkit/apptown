package deployment

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids"
)

func janitorReleaseWorld(t *testing.T) (*digestWorld, *Deployment, *ReleaseBundle, *Release) {
	t.Helper()
	w := newDigestWorld(t, "", nil)
	bundle := w.publish(t)
	first, err := w.promote(t, bundle.ID, "main", w.lab)
	if err != nil {
		t.Fatal(err)
	}
	dep, err := w.s.Deployments.ByName(t.Context(), w.project, "main")
	if err != nil {
		t.Fatal(err)
	}
	dep, err = w.s.SetFeed(t.Context(), w.project, dep.Name, FeedSettings{Revision: dep.FeedRevision, Feed: "main"})
	if err != nil {
		t.Fatal(err)
	}
	w.s.configuration.Authority = &requestAuthority{}
	return w, dep, bundle, first
}
func nextJanitorBundle(t *testing.T, w *digestWorld, base *ReleaseBundle) *ReleaseBundle {
	t.Helper()
	origin := base.BuiltFrom
	origin.RunID = ids.New(time.Now())
	origin.StepID = ids.New(time.Now())
	origin.Attempt = 1
	origin.By = &claimedAttempt{db: w.db, project: w.project, run: origin.RunID, step: origin.StepID, at: 1}
	id, err := w.s.Bundles.Cut(t.Context(), w.project, w.revision, base.Parts, base.Init, w.contract, origin)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := w.s.Bundles.ByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}
func requestJanitorBundle(t *testing.T, w *digestWorld, b *ReleaseBundle) *Release {
	t.Helper()
	if err := w.s.RequestLandingReleases(t.Context(), w.project, b.BuiltFrom.RunID, b.BuiltFrom.ChangeID, "the janitor, under standing approval test (rev 1), granted by alice", true); err != nil {
		t.Fatal(err)
	}
	rel, err := w.s.Releases.Last(t.Context(), w.project, "main")
	if err != nil || rel.BundleID != b.ID {
		t.Fatalf("routine request: %+v %v", rel, err)
	}
	metadata, err := w.s.Releases.RequestMetadata(t.Context(), rel.ID)
	if err != nil || !metadata.Routine {
		t.Fatalf("routine provenance missing: %+v %v", metadata, err)
	}
	return rel
}
func wantJanitorHold(t *testing.T, w *digestWorld, want bool) {
	t.Helper()
	held, err := w.s.HeldBack(t.Context(), w.project)
	if err != nil || (len(held) > 0) != want {
		t.Fatalf("held back=%t: %+v %v", want, held, err)
	}
}

func TestRoutineWindowUsesSettledReleasesAndRechecksBeforeExecution(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy window", true: "first release fails while second waits"}[fail], func(t *testing.T) {
			w, dep, base, _ := janitorReleaseWorld(t)
			now := time.Date(2026, 11, 15, 1, 0, 0, 0, time.UTC)
			w.s.clock = func() time.Time { return now }
			due := &routineDueQueue{times: map[ids.ID]time.Time{}}
			w.s.release.Queue = due
			_, err := w.s.SetRoutineUpdates(t.Context(), w.project, dep.Name, RoutineUpdateSettings{Revision: dep.RoutineUpdatesRevision, RoutineUpdates: RoutineUpdates{Mode: "window", Days: "sun", From: "02:00", To: "04:00", Zone: "UTC"}})
			if err != nil {
				t.Fatal(err)
			}
			b := nextJanitorBundle(t, w, base)
			first := requestJanitorBundle(t, w, b)
			c := nextJanitorBundle(t, w, b)
			second := requestJanitorBundle(t, w, c)
			if !due.times[first.ID].Equal(now.Add(time.Hour)) || !due.times[second.ID].Equal(now.Add(time.Hour)) {
				t.Fatalf("window jobs were not scheduled for the next start: %+v", due.times)
			}
			wantJanitorHold(t, w, false)
			if err := w.s.ExecuteRelease(t.Context(), first.ID); err == nil {
				t.Fatal("release ran before the window")
			}
			untouched, err := w.s.Releases.ByID(t.Context(), first.ID)
			if err != nil || untouched.State != Releasing {
				t.Fatalf("waiting window was failed: %+v %v", untouched, err)
			}
			now = now.Add(time.Hour)
			original := w.s.release.Sources
			if fail {
				w.s.release.Sources = &requestSources{err: errors.New("source unavailable")}
			}
			err = w.s.ExecuteRelease(t.Context(), first.ID)
			if (err != nil) != fail {
				t.Fatalf("execute first: %v", err)
			}
			w.s.release.Sources = original
			wantJanitorHold(t, w, fail)
			if err := w.s.ExecuteRelease(t.Context(), second.ID); err != nil {
				t.Fatal(err)
			}
			latest, err := w.s.Releases.ByID(t.Context(), second.ID)
			if err != nil {
				t.Fatal(err)
			}
			if fail && latest.State != releaseWaiting || !fail && latest.State != Released {
				t.Fatalf("second release ignored current health: %+v", latest)
			}
			if fail {
				decision, err := w.s.ReleaseDecision(t.Context(), latest)
				if err != nil || !decision.CanApprove || !strings.Contains(decision.WaitReason, "failed") {
					t.Fatalf("held release must explain its wait and offer a person release: %+v %v", decision, err)
				}
			}
			wantJanitorHold(t, w, fail)
		})
	}
}

func TestRetryAndRollbackKeepTheHoldUntilNewestBundleSucceeds(t *testing.T) {
	w, _, base, first := janitorReleaseWorld(t)
	newest := nextJanitorBundle(t, w, base)
	routine := requestJanitorBundle(t, w, newest)
	original := w.s.release.Sources
	w.s.release.Sources = &requestSources{err: errors.New("cannot prepare")}
	if err := w.s.ExecuteRelease(t.Context(), routine.ID); err == nil {
		t.Fatal("fixture did not fail the newest release")
	}
	w.s.release.Sources = original
	wantJanitorHold(t, w, true)
	pause := func(selection DeploySelection, after bool) {
		t.Helper()
		accepted, err := w.s.RequestRelease(t.Context(), selection)
		if err != nil {
			t.Fatal(err)
		}
		entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		underlying := w.s.release.Sources
		w.s.release.Sources = pausedRoutineSources{ReleaseSources: underlying, entered: entered, resume: resume}
		go func() { done <- w.s.ExecuteRelease(context.Background(), accepted.ID) }()
		select {
		case <-entered:
		case err := <-done:
			t.Fatalf("release ended before its effect: %v", err)
		case <-time.After(15 * time.Second):
			t.Fatal("release never reached its effect")
		}
		wantJanitorHold(t, w, true)
		close(resume)
		w.s.release.Sources = underlying
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("release did not finish")
		}
		wantJanitorHold(t, w, after)
		rel, err := w.s.Releases.ByID(t.Context(), accepted.ID)
		if err != nil || rel.RollbackOf.IsZero() {
			t.Fatalf("selection provenance missing: %+v %v", rel, err)
		}
	}
	// Try again selects the failed current-main release. RollbackOf is provenance,
	// so its successful recovery clears the hold despite that field.
	pause(DeploySelection{ProjectID: w.project, DeploymentName: "main", ReleaseNumber: routine.Number}, false)
	later := nextJanitorBundle(t, w, newest)
	failed := requestJanitorBundle(t, w, later)
	w.s.release.Sources = &requestSources{err: errors.New("cannot prepare later")}
	if err := w.s.ExecuteRelease(t.Context(), failed.ID); err == nil {
		t.Fatal("later release did not fail")
	}
	w.s.release.Sources = original
	pause(DeploySelection{ProjectID: w.project, DeploymentName: "main", ReleaseNumber: first.Number}, true)
	waiting := requestJanitorBundle(t, w, nextJanitorBundle(t, w, later))
	if waiting.State != releaseWaiting {
		t.Fatalf("routine release overwrote rollback: %+v", waiting)
	}
	manual, err := w.s.RequestRelease(t.Context(), DeploySelection{ProjectID: w.project, DeploymentName: "main", BundleID: later.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.ExecuteRelease(t.Context(), manual.ID); err != nil {
		t.Fatal(err)
	}
	wantJanitorHold(t, w, false)
}

func TestAnUncertainAttemptOfTheSuccessfulBundleHoldsTheProject(t *testing.T) {
	w, dep, b, _ := janitorReleaseWorld(t)
	accepted, err := w.s.RequestRelease(t.Context(), DeploySelection{ProjectID: w.project, DeploymentName: "main", BundleID: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	at, _, err := w.s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "uncertain-release", Inputs: Inputs{ReleaseID: accepted.ID, BundleID: b.ID, Revision: w.revision}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Outcomes.Uncertain(t.Context(), at, errors.New("lost provider response")); err != nil {
		t.Fatal(err)
	}
	rel, err := w.s.Releases.ByID(t.Context(), accepted.ID)
	if err != nil || rel.State != Releasing || !rel.ProgressUncertain {
		t.Fatalf("fixture is not uncertain: %+v %v", rel, err)
	}
	wantJanitorHold(t, w, true)
	waiting := requestJanitorBundle(t, w, nextJanitorBundle(t, w, b))
	if waiting.State != releaseWaiting {
		t.Fatalf("uncertain execution allowed routine release: %+v", waiting)
	}
}

func TestRoutineUpdateWindowUsesLocalDaysAndRejectsInvalidSettings(t *testing.T) {
	r := RoutineUpdates{Mode: "window", Days: "sun", From: "02:00", To: "04:00", Zone: "Australia/Melbourne"}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 14, 15, 30, 0, 0, time.UTC)
	if got := r.nextWindow(at); !got.Equal(at) {
		t.Fatalf("inside Melbourne Sunday window: %s", got)
	}
	at = at.Add(3 * time.Hour)
	if got := r.nextWindow(at); got.Sub(at) < 6*24*time.Hour {
		t.Fatalf("missed window did not wait a week: %s", got)
	}
	for _, bad := range []RoutineUpdates{{Mode: "window"}, {Mode: "window", Days: "someday", From: "02:00", To: "04:00", Zone: "UTC"}, {Mode: "automatic", Days: "sun"}, {Mode: "unknown"}} {
		if err := bad.validate(); err == nil {
			t.Fatalf("invalid settings accepted: %+v", bad)
		}
	}
}

type pausedRoutineSources struct {
	ReleaseSources
	entered chan struct{}
	resume  chan struct{}
}

func (p pausedRoutineSources) Contract(ctx context.Context, project ReleaseProject, revision string) (*appcontract.Contract, string, error) {
	close(p.entered)
	select {
	case <-p.resume:
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	return p.ReleaseSources.Contract(ctx, project, revision)
}

func TestRoutinePersonWaitsForAnExplicitPersonalRelease(t *testing.T) {
	w, dep, base, _ := janitorReleaseWorld(t)
	if _, err := w.s.SetRoutineUpdates(t.Context(), w.project, dep.Name, RoutineUpdateSettings{Revision: dep.RoutineUpdatesRevision, RoutineUpdates: RoutineUpdates{Mode: "person"}}); err != nil {
		t.Fatal(err)
	}
	rel := requestJanitorBundle(t, w, nextJanitorBundle(t, w, base))
	if rel.State != releaseWaiting {
		t.Fatalf("person setting released automatically: %+v", rel)
	}
	decision, err := w.s.ReleaseDecision(t.Context(), rel)
	if err != nil || !decision.CanApprove || decision.WaitReason != "waiting for a person to release this update" {
		t.Fatalf("a person cannot start the waiting release: %+v %v", decision, err)
	}
	if err := w.s.ApproveRelease(t.Context(), w.project, rel.Number, decision.Meaning); err != nil {
		t.Fatal(err)
	}
	if err := w.s.ExecuteRelease(t.Context(), rel.ID); err != nil {
		t.Fatal(err)
	}
	got, err := w.s.Releases.ByID(t.Context(), rel.ID)
	if err != nil || got.State != Released || got.Approval == nil {
		t.Fatalf("personal release did not settle: %+v %v", got, err)
	}
}

type routineDueQueue struct{ times map[ids.ID]time.Time }

func (q *routineDueQueue) EnqueueReleaseIn(_ context.Context, _ *database.Tx, id ids.ID, after time.Time) error {
	q.times[id] = after
	return nil
}

func TestRoutineProtectedDeploymentStillWaitsForAPerson(t *testing.T) {
	w, dep, base, _ := janitorReleaseWorld(t)
	target, err := w.s.Targets.ByID(t.Context(), dep.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.Targets.Offer(t.Context(), target, "*", OfferCaps{}, "setup"); err != nil {
		t.Fatal(err)
	}
	dep, err = w.s.SetClassification(t.Context(), w.project, dep.Name, edit(dep, "public", "synthetic", ""))
	if err != nil {
		t.Fatal(err)
	}
	if !dep.Protected() {
		t.Fatal("fixture is not protected")
	}
	rel := requestJanitorBundle(t, w, nextJanitorBundle(t, w, base))
	if rel.State != releaseWaiting {
		t.Fatalf("protected deployment released automatically: %+v", rel)
	}
}

func TestRoutineUpdateSettingsRefuseAStaleRevision(t *testing.T) {
	w, dep, _, _ := janitorReleaseWorld(t)
	in := RoutineUpdateSettings{Revision: dep.RoutineUpdatesRevision, RoutineUpdates: RoutineUpdates{Mode: "person"}}
	if _, err := w.s.SetRoutineUpdates(t.Context(), w.project, dep.Name, in); err != nil {
		t.Fatal(err)
	}
	in.RoutineUpdates.Mode = "automatic"
	if _, err := w.s.SetRoutineUpdates(t.Context(), w.project, dep.Name, in); err == nil {
		t.Fatal("stale settings overwrote a person's choice")
	}
	got, err := w.s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil || got.RoutineUpdates.Mode != "person" {
		t.Fatalf("choice changed: %+v %v", got, err)
	}
}

func TestRoutineRedeliveryCannotTurnClaimedExecutionIntoAWaitingRequest(t *testing.T) {
	w, dep, base, _ := janitorReleaseWorld(t)
	rel := requestJanitorBundle(t, w, nextJanitorBundle(t, w, base))
	pause := pausedRoutineSources{ReleaseSources: w.s.release.Sources, entered: make(chan struct{}), resume: make(chan struct{})}
	w.s.release.Sources = pause
	done := make(chan error, 1)
	go func() { done <- w.s.ExecuteRelease(t.Context(), rel.ID) }()
	select {
	case <-pause.entered:
	case <-time.After(15 * time.Second):
		t.Fatal("release did not begin preparation")
	}
	defer func() {
		if pause.resume != nil {
			close(pause.resume)
		}
	}()
	attempt, _, err := w.s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "uncertain-delivery", Inputs: Inputs{ReleaseID: rel.ID, BundleID: rel.BundleID, Revision: w.revision}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Outcomes.Uncertain(t.Context(), attempt, errors.New("lost provider response")); err != nil {
		t.Fatal(err)
	}
	wantJanitorHold(t, w, true)
	if err := w.s.ExecuteRelease(t.Context(), rel.ID); err != nil {
		t.Fatal(err)
	}
	current, err := w.s.Releases.ByID(t.Context(), rel.ID)
	if err != nil || current.State != Releasing {
		t.Fatalf("redelivery changed an already claimed release: %+v %v", current, err)
	}
	// The interrupted delivery retains its claim; the uncertain generation must
	// be recovered rather than prepared for a second time.
	close(pause.resume)
	pause.resume = nil
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("release did not finish")
	}
}
