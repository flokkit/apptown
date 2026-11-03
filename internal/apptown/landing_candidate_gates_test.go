package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/preview"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
)

// Real admission, repository preparation and steps, with dispatch stopped so
// each test can insert a change at the exact gate/merge boundary.
func reviewLanding(t *testing.T, a *App, steps []schema.Step, files map[string]string, beforeStart func(ids.ID)) (*workflow.Run, workflow.MergeSource) {
	t.Helper()
	pid := plainTestProject(t, a.DB, "review-landing", "Review Landing")
	p := a.projectByID(context.Background(), pid)
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "main", ""); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{
		{Name: "gates", Steps: steps},
		{Name: "done", Steps: []schema.Step{{Name: "hold", Uses: "approval", With: map[string]any{"means": "done"}}}},
	}}})
	if err := recordCatalogFixture(a.Adoption, t.Context(), p.ID, "review"); err != nil {
		t.Fatal(err)
	}
	ch := engineChange(t, a, p)
	if files != nil {
		if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
			t.Fatal(err)
		}
		ws := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
		for name, body := range files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(ws, name)), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
		}
		// The change's worker commits its work, ignored files included when
		// it forces them in; the landing publishes what is committed.
		gittest.Run(t, ws, "add", "-A", "-f")
		gittest.Run(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "the change's work")
	}
	if beforeStart != nil {
		beforeStart(p.ID)
	}
	a.Work.Stop()
	run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "land"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	return run, mergeSourceForTest(t, a, run)
}

func reviewExecute(t *testing.T, a *App, run *workflow.Run, name string) *workflow.Step {
	t.Helper()
	snap, err := a.Work.Snapshot(a.asAppTown(context.Background(), run.ProjectID, "review"), run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snap.Steps {
		if s.Name == name {
			if err := a.Work.ExecuteStep(a.asEngine(context.Background()), s.ID); err != nil {
				t.Fatal(err)
			}
			saved, err := workflow.ReadStepIn(context.Background(), a.DB, s.ID)
			if err != nil {
				t.Fatal(err)
			}
			return saved
		}
	}
	t.Fatalf("missing step %s", name)
	return nil
}

func TestAScriptGateInALandingChecksThePinnedCandidate(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	useScriptTasks(t, a, valueIs("good"))
	run, source := reviewLanding(t, a, []schema.Step{{Name: "source-check", Uses: "script", With: map[string]any{"run": `test "$(cat value.txt)" = good; result=$?; git checkout -- value.txt; exit "$result"`}}}, map[string]string{"value.txt": "bad\n"}, nil)
	if source.Landing.Candidate == "" {
		t.Fatal("fixture has no candidate")
	}
	if err := os.WriteFile(filepath.Join(requireLocation(t)(source.Workspace(a.cfg.dataDir)), "value.txt"), []byte("good\n"), 0644); err != nil {
		t.Fatal(err)
	}
	checked := reviewExecute(t, a, run, "source-check")
	result, err := mergeEffectForTest(t, a, source)
	if err == nil && result.Merged {
		got := gittest.Run(t, requireLocation(t)(source.Repository(a.cfg.dataDir)), "show", "HEAD:value.txt")
		t.Fatalf("merged %q after source-check status %s checked live 'good' bytes: %s", got, checked.Status, checked.Evidence)
	}
}

func TestARuntimeCheckIsNotAttributedToAnUnprovenPreview(t *testing.T) {
	t.Parallel()
	const given = `{"SENTRY_DSN":"https://key@errors.example/3"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_apptown/env.js" {
			w.Write([]byte(docker.BrowserEnvScript(given)))
			return
		}
		w.Write([]byte(`<html><script src="/_apptown/env.js"></script><script src="game.js"></script></html>`))
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	daemon := newFakeDocker()
	daemon.port, _ = strconv.Atoi(port)
	a := testApp(t, Config{DockerClient: daemon})
	run, source := reviewLanding(t, a, []schema.Step{{Name: "guidance", Uses: "guidance/conformance"}}, map[string]string{
		".apptown/app.yaml": "app:\n  web: {static: ., public: true}\n", "index.html": "<html>does not load browser values</html>",
	}, func(pid ids.ID) {
		adopt(t, a, pid, "deploy/browser-values", 1, `[{"appliesTo": ["repository"], "name":"page-loads-browser-values","expects":"loads the public values","evaluator":"runtime","probe":"browser-env","severity":"high"}]`)
		if err := a.Integrations.Attachments.Remember(t.Context(), pid, "errors", "glitchtip", `{"dsn":"https://key@errors.example/3"}`); err != nil {
			t.Fatal(err)
		}
	})
	if source.Landing.Candidate == "" {
		t.Fatal("fixture has no candidate")
	}
	// This unrelated preview is not a deployment of the pinned tree. No
	// revision/provenance ties its HTTP response to this landing.
	daemon.markStartedWithBrowser(docker.PreviewChangeName(source.ChangeID), given)
	refusePreviewBoots(daemon)
	saved := reviewExecute(t, a, run, "guidance")
	if saved.Status == workflow.StatusPass {
		t.Fatalf("a landing that could not judge its candidate passed: %s", saved.Evidence)
	}
	var ev struct {
		Candidate string
		Checks    verification.Results
	}
	if err := json.Unmarshal(saved.Evidence, &ev); err != nil {
		t.Fatal(err)
	}
	for _, r := range ev.Checks {
		if r.Check == "page-loads-browser-values" && r.Verdict == workflow.VerdictPass && ev.Candidate == source.Landing.Candidate {
			t.Fatalf("candidate whose page never loads browser values received another preview's pass: %s", saved.Evidence)
		}
	}
}

func TestGuidanceScopeAndReworkEvidenceUseTheCandidate(t *testing.T) {
	t.Parallel()
	for _, mutateIgnore := range []bool{false, true} {
		t.Run(fmt.Sprintf("change-live-ignore=%t", mutateIgnore), func(t *testing.T) {
			a := testApp(t, Config{DockerClient: newFakeDocker()})
			run, source := reviewLanding(t, a, []schema.Step{{Name: "catch-up", Uses: "reconcile"}, {Name: "guidance", Uses: "guidance/conformance"}}, map[string]string{
				".gitignore": "artifacts/\n", "artifacts/output.txt": "generated output", ".apptown/app.yaml": "app:\n  web: {static: ., public: true}\n",
			}, func(pid ids.ID) {
				adopt(t, a, pid, "deploy/browser-values", 1, `[{"appliesTo": ["repository"], "name":"page-loads-browser-values","expects":"loads the public values","evaluator":"runtime","probe":"browser-env","severity":"high"}]`)
			})
			// The change's work forced the ignored output into its commits
			// (reviewLanding), so the candidate carries it.
			ws := requireLocation(t)(source.Workspace(a.cfg.dataDir))
			caught := reviewExecute(t, a, run, "catch-up")
			if caught.Status != workflow.StatusPass {
				t.Fatalf("catch-up: %s %s", caught.Status, caught.Evidence)
			}
			source = mergeSourceForTest(t, a, run)
			if mutateIgnore {
				if err := os.WriteFile(filepath.Join(ws, ".gitignore"), nil, 0644); err != nil {
					t.Fatal(err)
				}
			}
			saved := reviewExecute(t, a, run, "guidance")
			if mutateIgnore {
				if saved.Status == workflow.StatusPass {
					t.Fatalf("live .gitignore concealed ignored files committed in candidate: %s", saved.Evidence)
				}
			} else {
				if saved.Status != workflow.StatusFail {
					t.Fatalf("control did not reject ignored files: %s %s", saved.Status, saved.Evidence)
				}
				var ev struct{ Candidate string }
				if err := json.Unmarshal(saved.Evidence, &ev); err != nil {
					t.Fatal(err)
				}
				if ev.Candidate != source.Landing.Candidate {
					t.Fatalf("failure evidence did not name checked candidate %s: %s", source.Landing.Candidate, saved.Evidence)
				}
			}
		})
	}
}

// landingWithAPage is a landing whose guidance gate has a runtime check of
// browser values, on an AppTown whose Docker serves a page that loads them;
// beforeLanding runs once the change is committed and before the landing
// starts.
func landingWithAPage(t *testing.T, beforeLanding func(*App, ids.ID)) (*App, *fakeDocker, *workflow.Run, workflow.MergeSource, string) {
	t.Helper()
	const given = `{"SENTRY_DSN":"https://key@errors.example/3"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_apptown/env.js" {
			w.Write([]byte(docker.BrowserEnvScript(given)))
			return
		}
		w.Write([]byte(`<html><script src="/_apptown/env.js"></script><script src="game.js"></script></html>`))
	}))
	t.Cleanup(server.Close)
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	daemon := newFakeDocker()
	daemon.port, _ = strconv.Atoi(port)
	a := testApp(t, Config{DockerClient: daemon, DockerProbe: &docker.Probe{Settle: time.Millisecond}})
	a.SyncStockDependencies(context.Background()) // error reporting's definition says what the page is given
	run, source := reviewLanding(t, a, []schema.Step{{Name: "guidance", Uses: "guidance/conformance"}}, map[string]string{
		".apptown/app.yaml": "app:\n  web: {static: ., public: true}\n", "index.html": "<html>the change's page</html>",
	}, func(pid ids.ID) {
		adopt(t, a, pid, "deploy/browser-values", 1, `[{"appliesTo": ["repository"], "name":"page-loads-browser-values","expects":"loads the public values","evaluator":"runtime","probe":"browser-env","severity":"high"}]`)
		if err := a.Integrations.Attachments.Remember(t.Context(), pid, "errors", "glitchtip", `{"dsn":"https://key@errors.example/3"}`); err != nil {
			t.Fatal(err)
		}
		if beforeLanding != nil {
			beforeLanding(a, pid)
		}
	})
	if source.Landing.Candidate == "" {
		t.Fatal("fixture has no candidate")
	}
	return a, daemon, run, source, given
}

// refusePreviewBoots makes every preview boot fail from here on. A landing
// boots its candidate's preview itself when none is proven to run it, and
// this daemon serves one page for every container: refused, the preview
// already running is the only thing that could answer the landing's checks,
// which is what the tests that use it judge.
func refusePreviewBoots(daemon *fakeDocker) {
	daemon.reply = func(args []string) (string, error, bool) {
		if len(args) > 0 && args[0] == "run" && strings.Contains(strings.Join(args, " "), "preview") {
			return "", errFakeExit, true
		}
		return "", nil, false
	}
}

// landingVerdict runs the landing's guidance gate and answers the browser
// values check's verdict and the candidate the gate names.
func landingVerdict(t *testing.T, a *App, run *workflow.Run) (workflow.CheckVerdict, string, string) {
	t.Helper()
	saved := reviewExecute(t, a, run, "guidance")
	var ev struct {
		Candidate string
		Checks    verification.Results
	}
	if err := json.Unmarshal(saved.Evidence, &ev); err != nil {
		t.Fatal(err)
	}
	for _, r := range ev.Checks {
		if r.Check == "page-loads-browser-values" {
			return r.Verdict, ev.Candidate, r.Detail
		}
	}
	t.Fatalf("the check was not judged: %s", saved.Evidence)
	return 0, "", ""
}

// A change being landed is previewed as its landing's pinned candidate, and
// the landing's runtime checks are judged against that preview: AppTown
// recorded booting it from the candidate itself. (A remediation of a
// runtime check could otherwise never land: its target is judged at the
// landing, which boots no preview of its own.)
func TestALandingsRuntimeCheckIsJudgedAgainstTheChangePreviewOfItsCandidate(t *testing.T) {
	t.Parallel()
	a, daemon, run, source, given := landingWithAPage(t, nil)
	candidate := source.Landing.Candidate
	// The folder holds something besides: the preview is the candidate's.
	if err := os.WriteFile(filepath.Join(requireLocation(t)(source.Workspace(a.cfg.dataDir)), "index.html"), []byte("an uncommitted edit"), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := a.Preview.RequestChangePreview(asLander(t, a), preview.Selection{ChangeID: source.ChangeID})
	if err != nil || out.Revision != candidate {
		t.Fatalf("a landing change's preview: %+v %v, want its candidate %s", out, err, candidate)
	}
	daemon.markStartedWithBrowser(docker.PreviewChangeName(source.ChangeID), given)
	if verdict, checked, detail := landingVerdict(t, a, run); verdict != workflow.VerdictPass || checked != candidate {
		t.Fatalf("the candidate's preview was not judged: %s %s (candidate %s)", verdict, detail, checked)
	}
}

// A preview AppTown booted from the change's folder before the landing is
// recorded, but not as the candidate: the landing does not judge it.
func TestALandingDoesNotJudgeTheChangesFolderPreview(t *testing.T) {
	t.Parallel()
	var changeID ids.ID
	a, daemon, run, source, given := landingWithAPage(t, func(a *App, pid ids.ID) {
		changes, err := a.Work.Changes(a.asAppTown(context.Background(), pid, "test"), pid, workflow.ChangeList{})
		if err != nil || len(changes) != 1 {
			t.Fatalf("the fixture's change: %v %v", changes, err)
		}
		changeID = changes[0].ID
		if _, err := a.Preview.RequestChangePreview(asLander(t, a), preview.Selection{ChangeID: changeID}); err != nil {
			t.Fatal(err)
		}
	})
	if changeID != source.ChangeID {
		t.Fatalf("booted change %d's preview, landing %d", changeID, source.ChangeID)
	}
	daemon.markStartedWithBrowser(docker.PreviewChangeName(source.ChangeID), given)
	refusePreviewBoots(daemon)
	if verdict, _, detail := landingVerdict(t, a, run); verdict == workflow.VerdictPass {
		t.Fatalf("the folder's preview was judged as the candidate: %s", detail)
	}
}

// Standing must use the landing's saved judgement of the checked tree, including
// old findings and exceptions, without asking any evaluator to judge it again.
func TestALandingsChecksBecomeItsStanding(t *testing.T) {
	for _, scenario := range []string{"pass", "preexisting", "exception", "main-moved", "missing-receipt", "AI"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			a := testApp(t)
			testAdministrator(t, a, "reviewer")
			var calls atomic.Int64
			var modelCalls atomic.Int64
			bindSecondaryFastSlot(t, a)
			installCheckEffects(t, a, checkOverrides{complete: func(string, string, string) (string, *modelcall.Usage, error) {
				modelCalls.Add(1)
				return `[{"item":1,"verdict":"pass","evidence":"work.txt:1","note":"one judgement"}]`, &modelcall.Usage{Prompt: 1, Completion: 1, Model: "fixture"}, nil
			}, run: func(req execRequest) execOutcome {
				calls.Add(1)
				return agentenv.Execute(a.processRunner, context.Background(), execution.Local, req, a.cfg.clock.Now())
			}})
			script := "echo landing-judgement; exit 0"
			if scenario == "preexisting" || scenario == "exception" {
				script = "echo saved-failure; exit 1"
			}
			var exception *conformance.Disposition
			run, source := reviewLanding(t, a, []schema.Step{{Name: "guidance", Uses: "guidance/conformance"}}, map[string]string{"work.txt": "changed\n"}, func(pid ids.ID) {
				checks := checksJSON(shCheck("rule", script, "high"))
				if scenario == "preexisting" {
					checks = strings.Replace(checks, `"severity":`, `"occurrences":"lines","severity":`, 1)
				}
				if scenario == "AI" {
					checks = `[{"appliesTo":["repository"],"name":"rule","evaluator":"llm","scope":"tree","files":["work.txt"],"rubric":"the change is clear","severity":"high","expects":"clear change"}]`
				}
				adopt(t, a, pid, "standing-check", 1, checks)
				if scenario == "exception" {
					var err error
					exception, err = a.conformance.CreateDisposition(testConformanceContext("reviewer"), conformance.ExceptionRequest{
						ProjectID: pid, Fingerprint: "guidance|standing-check@1|rule|", Kind: conformance.AcceptedRisk, Justification: "accepted for this project",
					})
					if err != nil {
						t.Fatal(err)
					}
				}
			})
			if scenario == "missing-receipt" {
				// The runner errored before producing this adopted check's receipt. A
				// non-gating error can still reach landing; it must not create a review.
				rebuildWork(t, a, func(d *workflow.Dependencies) {
					d.Runners["guidance/conformance"] = func(e *workflow.Execution) {
						if _, err := e.Claim(); err != nil {
							t.Error(err)
							return
						}
						if err := e.Finish(workflow.CheckEvidence{}.Outcome(workflow.StatusPass, map[string]any{"reason": "no receipt", "commit": source.Landing.Candidate, "candidate": source.Landing.Candidate})); err != nil {
							t.Error(err)
						}
					}
				})
				a.Work.Stop()
			}
			checked := reviewExecute(t, a, run, "guidance")
			if checked.Status != workflow.StatusPass {
				t.Fatalf("guidance: %s %s", checked.Status, checked.Evidence)
			}
			evidence, err := workflow.ReadCheckEvidence(checked.Evidence)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "preexisting" && (len(evidence.Checks) != 1 || !evidence.Checks[0].Preexisting) {
				t.Fatalf("fixture needs a pre-existing finding: %s", checked.Evidence)
			}
			before := calls.Load()
			if scenario == "AI" && modelCalls.Load() != 1 {
				t.Fatalf("want one AI judgement at the gate, got %d: %s", modelCalls.Load(), checked.Evidence)
			}
			repo := requireLocation(t)(source.Repository(a.cfg.dataDir))
			if scenario == "main-moved" {
				if err := os.WriteFile(filepath.Join(repo, "other.txt"), []byte("other landing"), 0644); err != nil {
					t.Fatal(err)
				}
				gittest.Run(t, repo, "add", "other.txt")
				gittest.Run(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "another landing")
			}
			result, err := mergeEffectForTest(t, a, source)
			ctx := testConformanceContext("reviewer")
			history, readErr := a.conformance.ProjectHistory(ctx, run.ProjectID, 40)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if scenario == "main-moved" {
				if err == nil || result.Merged || len(history) != 0 {
					t.Fatalf("refused landing recorded results: %+v %v %+v", result, err, history)
				}
				return
			}
			if err != nil || !result.Merged {
				t.Fatalf("landing: %+v %v", result, err)
			}
			if scenario == "missing-receipt" {
				reviews, readErr := a.conformance.Reviews(ctx, 40)
				if readErr != nil {
					t.Fatal(readErr)
				}
				for _, r := range reviews {
					if r.Trigger == "landing" && r.SourceRun == run.ID {
						t.Fatalf("incomplete receipts created a review: %+v", r)
					}
				}
				if len(history) != 0 {
					t.Fatalf("partial review: %+v", history)
				}
				return
			}
			if len(history) != 1 {
				t.Fatalf("want the landing's one saved assessment: %+v", history)
			}
			// A repeated merge observation must not become a second assessment.
			repeated, err := mergeEffectForTest(t, a, source)
			if err != nil || !repeated.Merged {
				t.Fatalf("repeat observation: %+v %v", repeated, err)
			}
			history, err = a.conformance.ProjectHistory(ctx, run.ProjectID, 40)
			if err != nil || len(history) != 1 {
				t.Fatalf("landing recorded twice: %+v %v", history, err)
			}
			landed := gittest.Run(t, repo, "rev-parse", "HEAD")
			detail, err := a.conformance.Review(ctx, history[0].ReviewID)
			if err != nil {
				t.Fatal(err)
			}
			r := detail.Review
			if r.Trigger != "landing" || r.Status != conformance.ReviewDone || r.SourceRun != run.ID || r.Candidate != source.Landing.Candidate {
				t.Fatalf("landing provenance: %+v", r)
			}
			if len(detail.Verdicts) != 1 {
				t.Fatalf("verdicts: %+v", detail.Verdicts)
			}
			v := detail.Verdicts[0].Record
			receipt := evidence.Checks[0]
			if v.SHA != landed || v.Mode != "gate" || v.Block != receipt.Block || v.Version != receipt.Version || v.Check != receipt.Check || v.EvaluatorVer != receipt.EvaluatorVer || v.EvaluatorVer == "" || v.Severity != "high" {
				t.Fatalf("saved identity: %+v, receipt %+v", v, receipt)
			}
			want := conformance.Conforms
			if scenario == "preexisting" {
				want = conformance.DoesNotConform
			}
			if scenario == "exception" {
				want = conformance.Excepted
			}
			assertStanding := func(want conformance.Outcome) *conformance.View {
				t.Helper()
				view, err := a.conformance.View(ctx, ids.Nil)
				if err != nil {
					t.Fatal(err)
				}
				got := view.ByProject["review-landing"]
				if len(got) != 1 || got[0].State != want || got[0].Record.ID != v.ID {
					t.Fatalf("current standing: %+v, want %s", got, want)
				}
				return view
			}
			assertStanding(want)
			if v.State != want {
				t.Fatalf("saved outcome: %+v", v)
			}
			if scenario == "exception" {
				historical, err := a.conformance.View(ctx, r.ID)
				if err != nil {
					t.Fatal(err)
				}
				if historical.ByProject["review-landing"][0].State != conformance.Excepted || len(historical.Exceptions["review-landing"]) != 1 || v.ExceptedBy == "" || !strings.Contains(v.Detail, "saved-failure") {
					t.Fatalf("lost exception or underlying failure: %+v %+v", v, historical.Exceptions)
				}
				if _, err := a.conformance.RevokeDisposition(ctx, exception.ID); err != nil {
					t.Fatal(err)
				}
				assertStanding(conformance.DoesNotConform)
				historical, err = a.conformance.View(ctx, r.ID)
				if err != nil || historical.ByProject["review-landing"][0].State != conformance.Excepted || len(historical.Exceptions["review-landing"]) != 1 {
					t.Fatalf("history changed after revocation: %+v %v", historical, err)
				}
			}
			if scenario == "AI" && (modelCalls.Load() != 1 || receipt.EvidenceHash == "" || receipt.EvidenceHash != v.EvidenceHash) {
				t.Fatalf("AI rejudged or evidence lost: calls=%d receipt=%+v saved=%+v", modelCalls.Load(), receipt, v)
			}
			if calls.Load() != before {
				t.Fatalf("landing or view evaluated again: %d -> %d", before, calls.Load())
			}
		})
	}
}
