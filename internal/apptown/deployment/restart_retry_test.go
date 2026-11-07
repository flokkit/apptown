package deployment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
)

// Losing the first provider checkpoint must not fail an ordinary restart.
// Recovery finds the same release generation and commits its receipt.
func TestRestartFinishesTheSameReleaseGeneration(t *testing.T) {
	s, _, db, pid, dep, bundle := doReleaseWorld(t)
	copy := &backupBeforeRelease{complete: true}
	s.release.Data = copy
	accepted := doAcceptRelease(t, s, pid, dep, bundle)
	dbtest.FailNextStatement(t, db, "UPDATE deployment_attempts SET observed_json")
	if err := s.ExecuteRelease(t.Context(), accepted.ID); err == nil {
		t.Fatal("checkpoint failure was not injected")
	}
	at, _, err := s.Outcomes.AttemptByKey(t.Context(), dep.ID, fmt.Sprintf("release-%d", accepted.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareRecovery(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := s.Recoverer().Run(t.Context(), at.ID)
	if err != nil || result.Outcome != "recovered" {
		t.Fatalf("restart retry: %+v %v", result, err)
	}
	rel, err := s.Releases.ByID(t.Context(), accepted.ID)
	if err != nil || rel.State != Released || rel.BundleID != bundle.ID || rel.Generation != at.Generation {
		t.Fatalf("same release receipt: %+v %v", rel, err)
	}
	settled, err := s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil || settled.DesiredGeneration != at.Generation || settled.ObservedGeneration != at.Generation {
		t.Fatalf("restart admitted a different generation: %+v %v", settled, err)
	}
	events, err := s.Releases.Events(t.Context(), rel.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		found = found || strings.Contains(e.Text, "interrupted by a restart")
	}
	if !found {
		t.Fatal("restart was not recorded in release history")
	}
	if strings.Join(copy.calls, ",") != "before" {
		t.Fatalf("restart repeated data protection effects: %v", copy.calls)
	}
	if _, err := s.Recoverer().Run(context.Background(), at.ID); err != nil {
		t.Fatal(err)
	}
}

// Durable consumption survives redelivery, including after an unassessable
// retry. Neither another worker nor another restart can execute it twice.
func TestRestartRetryIsConsumedOnceAcrossDeliveries(t *testing.T) {
	f, _, _, at := releaseOutcomeFixture(t)
	useRecoveryJobs(t, f)
	if claimed, err := f.outcomes.claimRestartRetry(t.Context(), at); err != nil || claimed {
		t.Fatalf("retry claimed before a restart: %v %v", claimed, err)
	}
	if _, err := PrepareRecovery(t.Context(), f.outcomes); err != nil {
		t.Fatal(err)
	}
	results := race(4, func(_ int) int {
		claimed, err := f.outcomes.claimRestartRetry(t.Context(), at)
		if err != nil {
			t.Error(err)
			return -1
		}
		if claimed {
			return 1
		}
		return 0
	})
	if count(results, 1) != 1 {
		t.Fatalf("retry executed more than once: %v", results)
	}
	if _, err := PrepareRecovery(t.Context(), f.outcomes); err != nil {
		t.Fatal(err)
	}
	if claimed, err := f.outcomes.claimRestartRetry(t.Context(), at); err != nil || claimed {
		t.Fatalf("restart renewed retry: %v %v", claimed, err)
	}
}

func TestRestartRetryRechecksTheOriginalRequestersAuthority(t *testing.T) {
	s, provider, db, pid, dep, bundle := doReleaseWorld(t)
	accepted := doAcceptRelease(t, s, pid, dep, bundle)
	dbtest.FailNextStatement(t, db, "UPDATE deployment_attempts SET observed_json")
	if err := s.ExecuteRelease(t.Context(), accepted.ID); err == nil {
		t.Fatal("checkpoint failure was not injected")
	}
	at, _, err := s.Outcomes.AttemptByKey(t.Context(), dep.ID, fmt.Sprintf("release-%d", accepted.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareRecovery(t.Context()); err != nil {
		t.Fatal(err)
	}
	provider.RemoveApp("app-1")
	before := provider.Snapshot().DeploymentGeneration
	s.release.Still = &requestAuthority{deny: errors.New("deploy permission withdrawn")}
	result, err := s.Recoverer().Run(t.Context(), at.ID)
	if err == nil || result.Outcome != "uncertain" {
		t.Fatalf("revoked retry: %+v %v", result, err)
	}
	if provider.Snapshot().DeploymentGeneration != before {
		t.Fatal("retry executed with revoked authority")
	}
	rel, err := s.Releases.ByID(t.Context(), accepted.ID)
	if err != nil || rel.State != Releasing {
		t.Fatalf("revocation invented provider failure: %+v %v", rel, err)
	}
}

// A cancelled Docker boot leaves its admitted identity, but no ready proof.
// The startup recovery must reuse the bundle and generation and finish it.
func TestRestartRetriesInterruptedDockerBootFromItsBundle(t *testing.T) {
	w := newDigestWorld(t, "", nil)
	bundle := w.publish(t)
	dep, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "staging", w.lab.ID, Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := w.s.RequestRelease(t.Context(), DeploySelection{ProjectID: w.project, DeploymentName: dep.Name, BundleID: bundle.ID})
	if err != nil {
		t.Fatal(err)
	}
	names, err := dep.HostNames()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	interrupted := false
	daemon := w.hosts.daemon("lab")
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "run" && strings.Contains(strings.Join(args, " "), names.App()) {
			interrupted = true
			cancel()
			return nil, context.Canceled, true
		}
		return nil, nil, false
	})
	err = w.s.ExecuteRelease(ctx, accepted.ID)
	daemon.SetReply(nil)
	if !interrupted || err == nil {
		t.Fatalf("boot was not interrupted: %v %v", interrupted, err)
	}
	at, status, err := w.s.Outcomes.AttemptByKey(t.Context(), dep.ID, fmt.Sprintf("release-%d", accepted.ID))
	if err != nil || status != AttemptUncertain {
		t.Fatalf("interrupted attempt: %s %v", status, err)
	}
	if _, err := w.s.PrepareRecovery(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := w.s.Recoverer().Run(t.Context(), at.ID)
	if err != nil || result.Outcome != "recovered" {
		t.Fatalf("Docker restart retry: %+v %v", result, err)
	}
	rel, err := w.s.Releases.ByID(t.Context(), accepted.ID)
	if err != nil || rel.State != Released || rel.BundleID != bundle.ID || rel.Generation != at.Generation {
		t.Fatalf("Docker retry receipt: %+v %v", rel, err)
	}
	in, err := w.s.Outcomes.InputsFor(t.Context(), at)
	if err != nil || in.ArtifactHash != deploy.ConfigHash(in.DockerSource) {
		t.Fatalf("retry changed selected artifact: %+v %v", in, err)
	}
	expectPulledOnly(t, w, "lab", bundle)
}

// The runtime check may settle observation, but the queued recovery job owns
// the apply retry and rechecks its original requester before those effects.
func TestCheckAgainLeavesTheRestartRetryToItsJob(t *testing.T) {
	s, provider, db, pid, dep, bundle := doReleaseWorld(t)
	s.authority = &requestAuthority{}
	accepted := doAcceptRelease(t, s, pid, dep, bundle)
	dbtest.FailNextStatement(t, db, "UPDATE deployment_attempts SET observed_json")
	if err := s.ExecuteRelease(t.Context(), accepted.ID); err == nil {
		t.Fatal("checkpoint failure was not injected")
	}
	at, _, err := s.Outcomes.AttemptByKey(t.Context(), dep.ID, fmt.Sprintf("release-%d", accepted.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareRecovery(t.Context()); err != nil {
		t.Fatal(err)
	}
	provider.RemoveApp("app-1")
	before := provider.Snapshot().DeploymentGeneration
	checked, err := s.CheckAgain(t.Context(), pid, dep.Name)
	if err != nil || len(checked.Checked) != 1 || checked.Checked[0].Outcome != "uncertain" {
		t.Fatalf("check again: %+v %v", checked, err)
	}
	if provider.Snapshot().DeploymentGeneration != before {
		t.Fatal("runtime check applied the queued retry")
	}
	recovered, err := s.Recoverer().Run(t.Context(), at.ID)
	if err != nil || recovered.Outcome != "recovered" {
		t.Fatalf("the runtime check consumed the recovery job's retry: %+v %v", recovered, err)
	}
}
