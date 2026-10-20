package forge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	dockerhost "flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func runDeploymentRecoveryForTest(t *testing.T, a *App, attemptID ids.ID) {
	t.Helper()
	runQueuedJobForTest(t, a, deployment.RecoveryJob, fmt.Sprint(attemptID))
}

func TestDeploymentRecoveryStartupQueuesBeforeProviderWork(t *testing.T) {
	t.Parallel()
	a, _, _, at := deploymentReleaseFixture(t)
	failNextJobInsert(t, a.DB)
	if err := a.RecoverDeployments(context.Background()); err == nil || !strings.Contains(err.Error(), "injected job insert failure") {
		t.Fatalf("startup ignored lost queue write: %v", err)
	}
	if got := attemptStatus(t, a, at.ID); got != deployment.AttemptRunning {
		t.Fatalf("attempt after a lost queue write: %s", got)
	}
	for i := 0; i < 2; i++ {
		report, err := deployment.PrepareRecovery(context.Background(), a.Deployment.Outcomes)
		if err != nil || report.Queued != 1 {
			t.Fatalf("startup report: %+v %v", report, err)
		}
	}
	// No target is configured: startup must not attempt to contact one.
	if n := jobsInState(t, a, deployment.RecoveryJob, "", "available"); n != 1 {
		t.Fatalf("%d available recovery jobs", n)
	}
	if got := attemptStatus(t, a, at.ID); got != deployment.AttemptUncertain {
		t.Fatalf("attempt after startup: %s", got)
	}
}

func TestDeploymentUncertaintyBlocksLandingAndBackgroundRebuild(t *testing.T) {
	t.Parallel()
	a, docker := bootWorld(t)
	a.SeedTargets(t.Context())
	_, stepID := deployRun(t, a)
	targetForLanding, err := a.Deployment.Targets.ByName(t.Context(), deployment.BuiltInTargetName)
	if err != nil {
		t.Fatal(err)
	}
	env, err := a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), "main", targetForLanding.ID, deployment.Attributes{Feed: "landing", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	s := a.Deployment.Outcomes
	at, _, err := s.Admit(context.Background(), deploymentStartFor(env, "interrupted", "original"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Uncertain(context.Background(), at, errors.New("lost provider response")); err != nil {
		t.Fatal(err)
	}
	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
	if step := readStep(t, a, stepID); step.Status != workflow.StatusError || !strings.Contains(stepReason(t, step), "uncertain") {
		t.Fatalf("landing deploy beside an uncertain one: %s %s", step.Status, step.Evidence)
	}
	if len(containersRun(docker)) != 0 {
		t.Fatal("landing booted before uncertainty admission guard")
	}
	a.ensureDeploymentsOnce(context.Background())
	if n := len(operationJobs(t, a, "rebuild")); n != 0 {
		t.Fatalf("%d rebuilds queued for an unresolved deployment", n)
	}
	if out, err := deploymentJobsForTest(a).Rebuild(context.Background(), "p/main", nil); err != nil || out["skipped"] != "deployment execution or observation owns this deployment" {
		t.Fatalf("queued rebuild bypassed recovery: %v %v", out, err)
	}
	if len(a.hosts.Watched(context.Background(), a.deployments)) != 0 {
		t.Fatal("health controller can act on unresolved deployment")
	}
	if len(containersRun(docker)) != 0 {
		t.Fatal("background work booted an unresolved deployment")
	}
}

func TestLandingDeploymentObservesRunningImageInsteadOfContractPin(t *testing.T) {
	t.Parallel()
	for _, lookupFailed := range []bool{false, true} {
		t.Run(fmt.Sprint(lookupFailed), func(t *testing.T) {
			a, docker := bootWorld(t)
			_, stepID := deployRun(t, a)
			docker.reply = func(args []string) (string, error, bool) {
				if len(args) > 2 && args[0] == "inspect" && args[2] == dockerhost.RuntimeFormat {
					if lookupFailed {
						return "", errFakeExit, true
					}
					docker.mu.Lock()
					labels := docker.labels[args[len(args)-1]]
					out := mustJSON(dockerhost.Runtime{Image: "sha256:actually-running", Running: true, Labels: labels})
					docker.mu.Unlock()
					return out, nil, true
				}
				if len(args) > 3 && args[0] == "image" && args[3] == "{{.Id}}" {
					return "sha256:actually-running\n", nil, true
				}
				return "", nil, false
			}
			a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
			if lookupFailed {
				if n := deploymentsMatching(t, a, func(d deployment.ProjectDeployment) bool {
					return d.ObservedGeneration == 0 && d.Readiness == deployment.ReadinessUncertain
				}); n != 1 {
					t.Fatalf("%d unobserved uncertain deployments", n)
				}
				if step := readStep(t, a, stepID); step.Status != workflow.StatusError {
					t.Fatalf("deploy step: %s", step.Status)
				}
			} else {
				if n := deploymentsMatching(t, a, func(d deployment.ProjectDeployment) bool {
					return d.ObservedGeneration == 1 && d.ImageDigest == "sha256:actually-running"
				}); n != 1 {
					t.Fatalf("%d deployments observed running the actual image", n)
				}
				if step := readStep(t, a, stepID); step.Status != workflow.StatusPass {
					t.Fatalf("deploy step: %s", step.Status)
				}
			}
		})
	}
}

// A restart mid-deploy (design/subsystems/deployment.md §14.7): the driver noted the
// app and deployment ids on the attempt as they appeared, so recovery
// finishes the attempt from them instead of failing it blind.
func TestRestartMidDeployFinishesTheAttemptFromWhatWasNoted(t *testing.T) {
	t.Parallel()
	a, f, p, b := doWorld(t)
	tg, _ := a.Deployment.Targets.ByName(t.Context(), "do-syd")
	env, err := a.Deployment.Deployments.EnsureStanding(t.Context(), p.ID, "cloud", tg.ID, deployment.Attributes{Feed: "promotion", Audience: "organisation", DataClass: "non-production", PromoteAfter: "main"})
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := requestBundleForTest(t, a, p, env, b, "bls")
	if err != nil {
		t.Fatal(err)
	}
	release, err := a.Deployment.Releases.ByNumber(context.Background(), p.ID, out["number"].(int64))
	if err != nil {
		t.Fatal(err)
	}
	if release.State != deployment.Released {
		t.Fatalf("first release ended %q", release.State)
	}
	// The notes landed on the attempt as the platform answered.
	firstAttempt, done, err := a.Deployment.Outcomes.AttemptByKey(context.Background(), env.ID, fmt.Sprintf("release-%d", release.ID))
	if err != nil || done != deployment.AttemptDone {
		t.Fatalf("first release's attempt: %s %v", done, err)
	}
	if noted := mustJSON(attemptNotes(t, a, firstAttempt.ID)); !strings.Contains(noted, `"appId":"app-1"`) || !strings.Contains(noted, `"deploymentId":"dep-1"`) {
		t.Fatalf("the attempt notes the platform's ids as they appear: %s", noted)
	}
	env, _ = a.Deployment.Deployments.ByName(t.Context(), p.ID, "cloud")

	// Now AppTown dies after the platform accepted generation 2 but
	// before it answered: a release on its way, a running attempt whose
	// notes name the app and the deployment.
	if !f.AdvanceDeployment("app-1", "dep-2") {
		t.Fatal("provider app was not created")
	}
	started, err := a.Deployment.Releases.Start(context.Background(), deployment.ReleaseDraft{Source: deployment.ReleaseFromBundle, ProjectID: p.ID, DeploymentID: env.ID, BundleID: b.ID, Deployment: env.Name, Revision: b.ContractRevision, Actor: "bls"}, "resuming after a restart")
	if err != nil {
		t.Fatal(err)
	}
	relID := started.ID
	attempt, _, err := (a.Deployment.Outcomes).Admit(context.Background(), deployment.Start{DeploymentID: env.ID, TargetID: env.TargetID, Key: fmt.Sprintf("release-%d", relID),
		Inputs: deployment.Inputs{ReleaseID: relID, Revision: b.ContractRevision}})
	attemptID, generation := attempt.ID, attempt.Generation
	if err != nil || generation != 2 {
		t.Fatalf("attempt: gen %d %v", generation, err)
	}
	if err := (a.Deployment.Outcomes).Checkpoint(context.Background(), attempt)(map[string]any{"appId": "app-1", "deploymentId": "dep-2"}); err != nil {
		t.Fatal(err)
	}

	if err := a.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := a.RecoverDeployments(context.Background()); err != nil {
		t.Fatal(err)
	}
	runDeploymentRecoveryForTest(t, a, attemptID)

	release, err = a.Deployment.Releases.ByNumber(context.Background(), p.ID, started.Number)
	if err != nil {
		t.Fatal(err)
	}
	_, status, _, err := a.Deployment.Outcomes.RecoveryAttempt(context.Background(), attemptID)
	if err != nil {
		t.Fatal(err)
	}
	env, _ = a.Deployment.Deployments.ByName(t.Context(), p.ID, "cloud")
	if release.State != deployment.Released || status != deployment.AttemptDone || env.ObservedGeneration != 2 || env.Readiness != deployment.Reconciled {
		t.Fatalf("after the restart: release %s attempt %s env gen %d %s", release.State, status, env.ObservedGeneration, env.Readiness)
	}
	events, err := a.Deployment.Releases.Events(context.Background(), relID)
	if err != nil {
		t.Fatal(err)
	}
	var eventText []string
	for _, event := range events {
		eventText = append(eventText, event.Text)
	}
	text := strings.Join(eventText, "\n")
	if !strings.Contains(text, "resuming after a restart: waiting on deployment dep-2") || !strings.Contains(text, "finished after a restart") {
		t.Fatalf("the release log says what happened:\n%s", text)
	}

	// An interruption before the provider named anything stays uncertain.
	// It cannot establish either success or failure.
	started2, err := a.Deployment.Releases.Start(context.Background(), deployment.ReleaseDraft{Source: deployment.ReleaseFromBundle, ProjectID: p.ID, DeploymentID: env.ID, BundleID: b.ID, Deployment: env.Name, Revision: b.ContractRevision, Actor: "bls"}, "interrupted before the platform named the app")
	if err != nil {
		t.Fatal(err)
	}
	relID2 := started2.ID
	second, _, err := (a.Deployment.Outcomes).Admit(context.Background(), deployment.Start{DeploymentID: env.ID, TargetID: env.TargetID, Key: fmt.Sprintf("release-%d", relID2),
		Inputs: deployment.Inputs{ReleaseID: relID2, Revision: b.ContractRevision}})
	if err != nil {
		t.Fatal(err)
	}
	attempt2 := second.ID
	if err := a.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := a.RecoverDeployments(context.Background()); err != nil {
		t.Fatal(err)
	}
	runDeploymentRecoveryForTest(t, a, attempt2)
	release, err = a.Deployment.Releases.ByNumber(context.Background(), p.ID, started2.Number)
	if err != nil {
		t.Fatal(err)
	}
	_, status, _, err = a.Deployment.Outcomes.RecoveryAttempt(context.Background(), attempt2)
	if err != nil {
		t.Fatal(err)
	}
	events, err = a.Deployment.Releases.Events(context.Background(), relID2)
	if err != nil {
		t.Fatal(err)
	}
	eventText = eventText[:0]
	for _, event := range events {
		eventText = append(eventText, event.Text)
	}
	text = strings.Join(eventText, "\n")
	env, _ = a.Deployment.Deployments.ByName(t.Context(), p.ID, "cloud")
	if release.State != deployment.Releasing || status != deployment.AttemptUncertain || env.Readiness != deployment.ReadinessUncertain || env.ObservedGeneration != 2 || !strings.Contains(text, "before the platform named the app") {
		t.Fatalf("blind attempt: release %s attempt %s gen %d\n%s", release.State, status, env.ObservedGeneration, text)
	}
}
