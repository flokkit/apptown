package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	testinputs "github.com/flokkit/apptown/internal/execution/executiontest"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"io"
	"net/http/httptest"
	"path/filepath"
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
func requestBundleForTest(t *testing.T, a *App, p *project.Project, env *deployment.Deployment, b *deployment.ReleaseBundle, who string, acceptUncopied ...bool) (map[string]any, int, error) {
	t.Helper()
	accepted, err := acceptBundleForTest(t, a, p, env, b, who, acceptUncopied...)
	if err != nil {
		return nil, 500, err
	}
	runQueuedJobForTest(t, a, deployment.ReleaseJob, fmt.Sprint(accepted.ID))
	return map[string]any{"number": accepted.Number, "deployment": accepted.Deployment, "state": accepted.State}, 202, nil
}

func acceptBundleForTest(t *testing.T, a *App, p *project.Project, env *deployment.Deployment, b *deployment.ReleaseBundle, who string, acceptUncopied ...bool) (deployment.AcceptedRelease, error) {
	t.Helper()
	if _, err := a.Access.EnsurePerson(context.Background(), who); err != nil {
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
	ctx := verifiedContext(context.Background(), access.Principal{ID: testPrincipalOf(t, a.DB, who), Kind: "user", Name: who, Via: "header", Credential: identity.HeaderCredential(who)})
	accepted, err := a.Deployment.RequestRelease(ctx, deployment.DeploySelection{ProjectID: p.ID, DeploymentName: env.Name, BundleID: b.ID, AcceptUncopiedData: len(acceptUncopied) > 0 && acceptUncopied[0]})
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
	// Binding setup can already have queued health. A refused release must
	// leave that work intact and add no job of its own.
	beforeJobs, err := jobs.List(t.Context(), a.DB, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeJobs) >= 500 {
		t.Fatal("job list reached its cap; a count over it is not a total")
	}
	completionExec(t, a.DB, `CREATE FUNCTION veto_release_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	completionExec(t, a.DB, `CREATE TRIGGER veto_release_job BEFORE INSERT ON river_job FOR EACH ROW EXECUTE FUNCTION veto_release_job()`)
	got, err := acceptBundleForTest(t, a, p, env, b, "alice")
	if err == nil || !got.ID.IsZero() {
		t.Fatalf("missing queue receipt accepted: %+v %v", got, err)
	}
	for _, table := range []string{"releases", "release_changes", "release_events", "release_requests", "river_job"} {
		want := 0
		if table == "river_job" {
			want = len(beforeJobs)
		}
		// sql-guarantee: the refused queue write must roll back every release row; only whole-table counts show nothing hidden was written.
		completionCount(t, a.DB, want, `SELECT COUNT(*) FROM `+table)
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
				completionExec(t, a.DB, `UPDATE release_requests SET claimed_at=now() WHERE release_id=$1`, got.ID)
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
	if w.Code != 403 || !strings.Contains(w.Body.String(), "ship a bundle") {
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

// The real release and data owners hand restored data directly to the selected
// code. The rejected code corrupts a sentinel whenever it starts; restarting it
// even briefly during the handoff therefore makes this test fail.
func TestPairedRollbackNeverStartsRejectedCodeOnRestoredData(t *testing.T) {
	for _, failBeforeStart := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail-before-start=%t", failBeforeStart), func(t *testing.T) {
			t.Parallel()
			w := newReleaseWorld(t, nil)
			w.lab2.Capabilities.KeepsDataDir = true
			var targetErr error
			w.lab2, targetErr = w.a.Deployment.Targets.Put(t.Context(), w.lab2)
			if targetErr != nil {
				t.Fatal(targetErr)
			}
			w.appYAML = "app:\n  web:\n    build: worker\n    public: true\n    ports: [\"8080\"]\n    dataDir: /data\n"
			w.mainOn(t, w.lab)
			w.landingRun(t)
			revision, err := gitexec.Run(t.Context(), w.a.projectRepoPath("p"), "rev-parse", "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			earlierImage := "sha256:" + strings.Repeat("a", 64)
			rejectedImage := "sha256:" + strings.Repeat("b", 64)
			cut := func(image string) ids.ID {
				b, err := w.a.Deployment.Bundles.Cut(t.Context(), idstest.N(1), strings.TrimSpace(revision), map[string]string{"web": image}, nil, []byte(w.appYAML), deployment.BundleOrigin{TargetID: w.lab2.ID})
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			earlierBundle, rejectedBundle := cut(earlierImage), cut(rejectedImage)
			dep, err := w.a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), "staging", w.lab2.ID, deployment.Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"})
			if err != nil {
				t.Fatal(err)
			}
			names, err := dep.HostNames()
			if err != nil {
				t.Fatal(err)
			}
			daemon := w.hosts.daemon("lab2")
			sentinel, currentImage, earlierSaw := "initial", "", ""
			restored, rejectedStartsAfterRestore := false, 0
			daemon.command = func(ctx context.Context, host string, req docker.Command) ([]byte, error) {
				args := req.Args
				if len(args) >= 2 && args[0] == "--config" {
					args = args[2:]
				}
				if req.Stdout != nil {
					if restored && failBeforeStart {
						return nil, fmt.Errorf("copy failed before selected code starts")
					}
					_, err := containerresulttest.WriteString(req.Stdout, sentinel)
					return nil, err
				}
				if req.Stdin != nil {
					raw, err := io.ReadAll(req.Stdin)
					if err != nil {
						return nil, err
					}
					sentinel = string(raw)
					restored = true
					return nil, nil
				}
				if len(args) == 4 && args[0] == "inspect" && args[2] == "{{.State.Running}}" {
					daemon.mu.Lock()
					running := daemon.started[args[3]]
					daemon.mu.Unlock()
					return []byte(fmt.Sprintf("%t\n", running)), nil
				}
				if len(args) == 2 && (args[0] == "stop" || args[0] == "start") {
					daemon.mu.Lock()
					daemon.started[args[1]] = args[0] == "start"
					daemon.mu.Unlock()
				}
				starts := len(args) > 0 && ((args[0] == "run" && containerNameOf(strings.Join(args, " ")) == names.App()) || (args[0] == "start" && args[len(args)-1] == names.App()))
				if starts {
					for _, arg := range args {
						if arg == earlierImage || arg == rejectedImage {
							currentImage = arg
						}
					}
					if currentImage == rejectedImage {
						if restored {
							rejectedStartsAfterRestore++
						}
						sentinel = "corrupted by rejected code"
					} else if restored {
						earlierSaw = sentinel
					}
				}
				return daemon.run(args...)
			}
			first, err := w.promote(t, earlierBundle, "staging", w.lab2)
			if err != nil {
				t.Fatal(err)
			}
			sentinel = "before"
			second, err := w.promote(t, rejectedBundle, "staging", w.lab2)
			if err != nil {
				t.Fatal(err)
			}
			if second.BackupID.IsZero() || sentinel != "corrupted by rejected code" {
				t.Fatalf("fixture: %+v sentinel=%q", second, sentinel)
			}
			backup, err := w.a.backupRecords.ForDeployment(t.Context(), idstest.N(1), "staging", 50)
			if err != nil {
				t.Fatal(err)
			}
			recorded := false
			for _, b := range backup {
				if b.ID == second.BackupID {
					recorded = b.RunningReleaseID == first.ID
				}
			}
			if !recorded {
				t.Fatal("pre-deploy backup did not record the earlier running release")
			}
			response := authzCall(w.a.Handler(), "boss", "POST", "/api/v1/projects/p/deployments/staging/deploy", fmt.Sprintf(`{"beforeRelease":%d}`, second.Number))
			var accepted struct {
				Number int64 `json:"number"`
			}
			if response.Code != 202 || json.Unmarshal(response.Body.Bytes(), &accepted) != nil {
				t.Fatalf("rollback admission: %d %s", response.Code, response.Body.String())
			}
			latest, err := w.a.Deployment.Releases.ByNumber(t.Context(), idstest.N(1), accepted.Number)
			if err != nil {
				t.Fatal(err)
			}
			runQueuedJobForTest(t, w.a, deployment.ReleaseJob, latest.ID.String())
			latest, err = w.a.Deployment.Releases.ByNumber(t.Context(), idstest.N(1), accepted.Number)
			if err != nil || rejectedStartsAfterRestore != 0 || latest.RollbackOf != first.ID || latest.RestoreBackupID != second.BackupID {
				t.Fatalf("rollback: %+v %v rejected starts=%d", latest, err, rejectedStartsAfterRestore)
			}
			if failBeforeStart {
				events, err := w.a.Deployment.Releases.Events(t.Context(), latest.ID)
				if err != nil || latest.State != deployment.ReleaseFailed || sentinel != "before" || earlierSaw != "" || !strings.Contains(events[len(events)-1].Text, "app remains stopped") {
					t.Fatalf("failed rollback: %+v sentinel=%q earlier saw=%q events=%v err=%v", latest, sentinel, earlierSaw, events, err)
				}
				daemon.mu.Lock()
				running := daemon.started[names.App()]
				daemon.mu.Unlock()
				if running {
					t.Fatal("failed rollback restarted an app")
				}
				for _, path := range []string{fmt.Sprintf("/api/v1/projects/p/releases/%d", latest.Number), "/api/v1/projects/p/releases"} {
					response := authzCall(w.a.Handler(), "boss", "GET", path, "")
					if response.Code != 200 || !strings.Contains(response.Body.String(), "app remains stopped") {
						t.Fatalf("stopped failure missing from %s: %d %s", path, response.Code, response.Body.String())
					}
				}
			} else if latest.State != deployment.Released || earlierSaw != "before" {
				t.Fatalf("selected code did not start on restored data: %+v sentinel=%q earlier saw=%q", latest, sentinel, earlierSaw)
			}
		})
	}
}

// Web selection and the real CLI both retry a failed follows-main release
// through the checked HTTP door, preserving its bundle and failed history.
func TestFailedReleaseRetryThroughWebAndCLI(t *testing.T) {
	a, _, p, bundle := doWorld(t)
	testAdministrator(t, a, "alice")
	target, err := a.Deployment.Targets.ByName(t.Context(), "do-syd")
	if err != nil {
		t.Fatal(err)
	}
	dep, err := a.Deployment.Deployments.EnsureStanding(t.Context(), p.ID, "main", target.ID, deployment.Attributes{Feed: "main", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	original, err := acceptBundleForTest(t, a, p, dep, bundle, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Outcomes.FailUnstartedRelease(t.Context(), original.ID, errors.New("provider refused")); err != nil {
		t.Fatal(err)
	}
	web := authzCall(a.Handler(), "alice", "POST", "/api/v1/projects/"+p.Slug+"/deployments/main/deploy", fmt.Sprintf(`{"releaseNumber":%d}`, original.Number))
	if web.Code != 202 {
		t.Fatalf("web retry: %d %s", web.Code, web.Body.String())
	}
	var accepted struct {
		Number int64 `json:"number"`
	}
	if err := json.Unmarshal(web.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	retried, err := a.Deployment.Releases.ByNumber(t.Context(), p.ID, accepted.Number)
	if err != nil || retried.BundleID != bundle.ID || retried.RollbackOf != original.ID {
		t.Fatalf("web retry receipt: %+v %v", retried, err)
	}
	if _, err := a.Deployment.Outcomes.FailUnstartedRelease(t.Context(), retried.ID, errors.New("provider refused again")); err != nil {
		t.Fatal(err)
	}
	caller := access.Principal{ID: testPrincipalOf(t, a.DB, "alice"), Kind: "user", Name: "alice", Via: "session"}
	token, err := a.Access.IssueToken(t.Context(), identity.NewToken{Name: "retry-cli", User: "alice", Purpose: "cli", Role: "member"}, caller)
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(t.TempDir(), "apptown")
	testinputs.GoBuild(t, "", nil, "../../cmd/apptown")
	runner := execution.Runner{}
	if out := runner.Run(t.Context(), execution.Local, execution.Request{Argv: []string{"go", "build", "-o", cli, "../../cmd/apptown"}, Timeout: time.Minute}); !out.Exited0() {
		t.Fatalf("build CLI: %s", out.Output)
	}
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	out := runner.Run(t.Context(), execution.Local, execution.Request{Argv: []string{cli, "release", p.Slug, "main", "--json"}, Env: []string{"APPTOWN_URL=" + srv.URL, "APPTOWN_TOKEN=" + token.Plaintext}, Timeout: time.Minute})
	if !out.Exited0() {
		t.Fatalf("CLI retry: %s", out.Output)
	}
	if err := json.Unmarshal(out.Output, &accepted); err != nil {
		t.Fatal(err)
	}
	last, err := a.Deployment.Releases.ByNumber(t.Context(), p.ID, accepted.Number)
	if err != nil || last.BundleID != bundle.ID || last.RollbackOf != retried.ID || last.ID == retried.ID {
		t.Fatalf("CLI retry receipt: %+v %v", last, err)
	}
	failed, err := a.Deployment.Releases.ByID(t.Context(), original.ID)
	if err != nil || failed.State != deployment.ReleaseFailed {
		t.Fatalf("retry rewrote failed history: %+v %v", failed, err)
	}
}
