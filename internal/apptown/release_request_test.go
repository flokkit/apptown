package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivertype"
)

// Provider fixtures exercise the real checked request and its real queued worker.
// Keep their provider assertions while avoiding detached goroutine timing.
func requestBundleForTest(t *testing.T, a *App, p *project.Project, env *deployment.Deployment, b *deployment.ReleaseBundle, who string) (map[string]any, int, error) {
	t.Helper()
	accepted, err := acceptBundleForTest(t, a, p, env, b, who)
	if err != nil {
		return nil, 500, err
	}
	runQueuedJobForTest(t, a, deployment.ReleaseJob, fmt.Sprint(accepted.ID))
	return map[string]any{"number": accepted.Number, "deployment": accepted.Deployment, "state": accepted.State}, 202, nil
}

func acceptBundleForTest(t *testing.T, a *App, p *project.Project, env *deployment.Deployment, b *deployment.ReleaseBundle, who string) (deployment.AcceptedRelease, error) {
	t.Helper()
	if _, err := a.Access.EnsureUser(context.Background(), who); err != nil {
		t.Fatal(err)
	}
	for _, grant := range []identity.Spec{
		{SubjectType: "user", Subject: who, Capability: string(access.CapDeploymentDeploy), Scope: access.ScopeProject, ScopeID: fmt.Sprint(p.ID), By: "setup"},
		{SubjectType: "user", Subject: who, Capability: string(access.CapReleasePromote), Scope: access.ScopeTarget, ScopeID: env.Name, By: "setup"},
	} {
		if _, err := a.Access.Grant(context.Background(), grant, ""); err != nil {
			t.Fatal(err)
		}
	}
	ctx := verifiedContext(context.Background(), access.Principal{Kind: "user", Name: who, Via: "session"})
	accepted, err := a.Deployment.RequestRelease(ctx, deployment.DeploySelection{ProjectID: p.ID, DeploymentName: env.Name, BundleID: b.ID})
	if err != nil {
		return deployment.AcceptedRelease{}, err
	}
	return accepted, nil
}

func TestReleaseRequestRiverInsertIsAtomic(t *testing.T) {
	t.Parallel()
	a, _, p, b := doWorld(t)
	target, err := a.Deployment.Targets.ByName(t.Context(), "do-syd")
	if err != nil {
		t.Fatal(err)
	}
	env, err := a.Deployment.Deployments.EnsureStanding(t.Context(), p.ID, "cloud", target.ID, (deployment.Attributes{}).WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	completionExec(t, a.DB, `CREATE FUNCTION veto_release_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	completionExec(t, a.DB, `CREATE TRIGGER veto_release_job BEFORE INSERT ON river_job FOR EACH ROW EXECUTE FUNCTION veto_release_job()`)
	got, err := acceptBundleForTest(t, a, p, env, b, "alice")
	if err == nil || !got.ID.IsZero() {
		t.Fatalf("missing queue receipt accepted: %+v %v", got, err)
	}
	for _, table := range []string{"releases", "release_changes", "release_events", "release_requests", "river_job"} {
		// sql-guarantee: the refused queue write must roll back every release row; only whole-table counts show nothing hidden was written.
		completionCount(t, a.DB, 0, `SELECT COUNT(*) FROM `+table)
	}
	completionExec(t, a.DB, `DROP TRIGGER veto_release_job ON river_job`)
	got, err = acceptBundleForTest(t, a, p, env, b, "alice")
	if err != nil || got.Number != 1 {
		t.Fatalf("retry: %+v %v", got, err)
	}
	queued, err := a.Jobs().Client.JobList(context.Background(), river.NewJobListParams().First(1000))
	if err != nil {
		t.Fatal(err)
	}
	single := 0
	for _, row := range queued.Jobs {
		request := exactRequest(row.EncodedArgs)
		if request.Operation == deployment.ReleaseJob && request.Key == fmt.Sprint(got.ID) && row.MaxAttempts == 1 {
			single++
		}
	}
	if single != 1 {
		t.Fatalf("%d single-attempt release jobs for the release", single)
	}
}

func TestReleaseRequestStartupOwnership(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"queued", "claimed", "cancelled", "discarded", "completed", "interrupted", "interrupted with attempt", "current owner"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			a, _, p, b := doWorld(t)
			target, err := a.Deployment.Targets.ByName(t.Context(), "do-syd")
			if err != nil {
				t.Fatal(err)
			}
			env, err := a.Deployment.Deployments.EnsureStanding(t.Context(), p.ID, "cloud", target.ID, (deployment.Attributes{}).WithDefaults())
			if err != nil {
				t.Fatal(err)
			}
			got, err := acceptBundleForTest(t, a, p, env, b, "alice")
			if err != nil {
				t.Fatal(err)
			}
			interrupted := scenario == "interrupted" || scenario == "interrupted with attempt"
			if interrupted || scenario == "current owner" {
				owner := "dead-release-owner"
				if scenario == "current owner" {
					owner = a.Jobs().Client.ID()
				}
				claimAvailableJobs(t, a.DB, owner, 1)
			}
			if scenario == "interrupted with attempt" {
				if _, _, err := a.Deployment.Outcomes.Admit(context.Background(), deployment.Start{DeploymentID: env.ID, TargetID: env.TargetID, Key: fmt.Sprintf("release-%d", got.ID), Inputs: deployment.Inputs{ReleaseID: got.ID, Revision: b.ContractRevision}}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "claimed" {
				// sql-guarantee: Model a process lost after request claim but before any provider effect; ExecuteRelease proceeds into placement/execution, and the isolated claim mutation is private to Deployment.
				completionExec(t, a.DB, `UPDATE release_requests SET claimed_at=now() WHERE release_id=?`, got.ID)
			}
			if scenario == "cancelled" || scenario == "discarded" || scenario == "completed" {
				pending, err := a.Jobs().Client.JobList(context.Background(), river.NewJobListParams())
				if err != nil {
					t.Fatal(err)
				}
				selected := 0
				for _, row := range pending.Jobs {
					var request jobs.Request
					if err := json.Unmarshal(row.EncodedArgs, &request); err != nil {
						t.Fatal(err)
					}
					if request.Operation != deployment.ReleaseJob {
						continue
					}
					finished := time.Now()
					if _, err := riverdatabasesql.New(a.DB.Pool()).GetExecutor().JobUpdateFull(context.Background(), &riverdriver.JobUpdateFullParams{ID: row.ID, StateDoUpdate: true, State: rivertype.JobState(scenario), FinalizedAtDoUpdate: true, FinalizedAt: &finished}); err != nil {
						t.Fatal(err)
					}
					selected++
				}
				if selected != 1 {
					t.Fatalf("settle release job: selected %d", selected)
				}
			}
			for i := 0; i < 2; i++ {
				if err := a.RecoverInterruptedWork(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			want := deployment.ReleaseFailed
			if scenario == "queued" || scenario == "current owner" || scenario == "interrupted with attempt" {
				want = deployment.Releasing
			}
			requireReleaseState(t, a, p.ID, got.Number, want)
			if scenario == "queued" {
				runQueuedJobForTest(t, a, deployment.ReleaseJob, fmt.Sprint(got.ID))
				requireReleaseState(t, a, p.ID, got.Number, deployment.Released)
			} else if want == deployment.ReleaseFailed {
				if err := a.Deployment.ExecuteRelease(context.Background(), got.ID); err != nil {
					t.Fatal(err)
				}
				// sql-guarantee: no owner read lists a deployment's attempts; a failed release must admit none of any key.
				completionCount(t, a.DB, 0, `SELECT COUNT(*) FROM deployment_attempts`)
			}
		})
	}
}

// Release refusals speak plainly and check the deploy target FIRST — a
// misconfigured instance is told about its missing connector, not about
// its repository.
func TestReleaseRefusalLadder(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")

	plainTestProject(t, a.DB, "p", "P")
	h := a.Handler()

	req := httptest.NewRequest("POST", "/api/v1/projects/p/release", strings.NewReader(`{}`))
	req.Header.Set("X-AppTown-Actor", "bls")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 422 {
		t.Fatalf("release without a deploy target must 422: %d %s", w.Code, w.Body.String())
	}

	// A refused release keeps nothing it asked for: the hostname it named
	// is not saved for later releases.
	if w := authzCall(h, "bls", "POST", "/api/v1/projects/p/release", `{"domain":"new.example"}`); w.Code != 422 {
		t.Fatalf("a refused release naming a hostname: %d %s", w.Code, w.Body.String())
	}
	if p, err := a.Projects.BySlug(context.Background(), "p"); err != nil || p.ReleaseDomain != "" {
		t.Fatalf("a refused release saved its hostname: %+v %v", p, err)
	}
	for _, body := range []string{`{"domain":`, `{"domain":42}`} {
		w := authzCall(h, "bls", "POST", "/api/v1/projects/p/release", body)
		if w.Code != 400 {
			t.Fatalf("malformed release: %d %s", w.Code, w.Body.String())
		}
		p, err := a.Projects.BySlug(context.Background(), "p")
		if err != nil || p.ReleaseDomain != "" {
			t.Fatalf("malformed input changed domain: %+v %v", p, err)
		}
	}
	// An ungranted person is refused before connector configuration is read.
	w = authzCall(h, "outsider", "POST", "/api/v1/projects/p/release", `{}`)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "release") {
		t.Fatalf("ungranted release: %d %s", w.Code, w.Body.String())
	}

}

// requireReleaseState fails unless the project's numbered release reads in the state.
func requireReleaseState(t *testing.T, a *App, projectID ids.ID, number int64, want deployment.ReleaseState) {
	t.Helper()
	release, err := a.Deployment.Releases.ByNumber(context.Background(), projectID, number)
	if err != nil {
		t.Fatal(err)
	}
	if release.State != want {
		t.Fatalf("release %d: %s, want %s", number, release.State, want)
	}
}
