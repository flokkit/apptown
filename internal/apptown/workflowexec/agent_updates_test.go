package workflowexec

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/stock"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
)

func npmRegistry(t *testing.T) (string, *atomic.Bool) {
	t.Helper()
	published := new(atomic.Bool)
	tarballs := map[string][]byte{}
	integrities := map[string]string{}
	for _, name := range []string{"example", "untargeted"} {
		for _, version := range []string{"1.0.0", "1.0.1", "2.0.0"} {
			raw := []byte(fmt.Sprintf(`{"name":%q,"version":%q}`, name, version))
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			if err := tw.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0644, Size: int64(len(raw))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(raw); err != nil {
				t.Fatal(err)
			}
			api := "original"
			if version == "2.0.0" {
				api = "renamed"
			}
			javascript := []byte("exports." + api + " = () => 'example';\n")
			if err := tw.WriteHeader(&tar.Header{Name: "package/index.js", Mode: 0644, Size: int64(len(javascript))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(javascript); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			key := name + "-" + version
			tarballs[key] = buf.Bytes()
			sum := sha512.Sum512(buf.Bytes())
			integrities[key] = "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
		}
	}
	var url string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if strings.Contains(path, "/-/") {
			parts := strings.Split(path, "/-/")
			key := strings.TrimSuffix(parts[1], ".tgz")
			data, ok := tarballs[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(data)
			return
		}
		if path != "example" && path != "untargeted" {
			http.NotFound(w, r)
			return
		}
		versions := map[string]any{}
		latest := "1.0.0"
		for _, v := range []string{"1.0.0", "1.0.1", "2.0.0"} {
			if v != "1.0.0" && !published.Load() {
				continue
			}
			latest = v
			versions[v] = map[string]any{"name": path, "version": v, "dist": map[string]string{"tarball": url + "/" + path + "/-/" + path + "-" + v + ".tgz", "integrity": integrities[path+"-"+v]}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"name": path, "dist-tags": map[string]string{"latest": latest}, "versions": versions})
	}))
	url = server.URL
	t.Cleanup(server.Close)
	return url, published
}

func fixtureShell(t *testing.T, folder string, argv []string, env []string) ([]byte, int) {
	t.Helper()
	out := (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{Workspace: folder, Argv: argv, Env: env, Timeout: 30 * time.Second})
	if !out.Answered() {
		t.Fatalf("fixture command did not answer: %v %s", out.RunErr, out.Output)
	}
	return out.Output, out.ExitCode
}

type updateJudgeFunc func(context.Context, workflow.Source, check.Item, string) (workflow.CheckReceipt, error)

func (f updateJudgeFunc) JudgeUpdate(ctx context.Context, s workflow.Source, it check.Item, diff string) (workflow.CheckReceipt, error) {
	return f(ctx, s, it, diff)
}

func newNodeFixFixture(t *testing.T, name string, spec schema.Spec, options ...func(*workflow.Dependencies)) (*taskAppTown, *atomic.Int32) {
	t.Helper()
	f, _ := newTaskAppTown(t, name, spec, options...)
	f.bound = "routine"
	f.agents.adoption = adoption.New(f.db, adoption.Dependencies{Ways: f.ways})
	f.ways.SyncStockGuidance(t.Context())
	block, err := f.ways.BlockByName(t.Context(), "lang/node/dependency-updates")
	if err != nil {
		t.Fatal(err)
	}
	receipt := adoption.EmptyReceipt()
	receipt.Guidance = append(receipt.Guidance, adoption.ReceiptBlock{Name: block.Name, BlockID: block.ID, Version: block.LatestVersion})
	if err := f.db.InTx(t.Context(), func(tx *database.Tx) error {
		return adoption.RecordIn(f.ctx, tx, f.projectID, 2, adoption.AdoptionEdit, "adopt dependency guidance", receipt, taskAppTownAdministrator, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"package.json": `{"name":"app","dependencies":{"example":"^1.0.0"}}`, "package-lock.json": `{"packages":{"node_modules/example":{"version":"1.0.0","resolved":"https://registry.example/example/-/example-1.0.0.tgz"}}}`, ".apptown/app.yaml": "app:\n  web:\n    build: .\n    public: true\n    tools: [node]\n    test: [\"node test.js\"]\n"} {
		path := filepath.Join(f.hub, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	gittest.Run(t, f.hub, "add", ".")
	gittest.Run(t, f.hub, "-c", "user.name=fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "dependencies")
	calls := new(atomic.Int32)
	installFakePi(f, func(_ int, req execution.Request) execution.Outcome {
		calls.Add(1)
		if err := os.WriteFile(filepath.Join(req.Workspace, "package-lock.json"), []byte(`{"packages":{"node_modules/example":{"version":"1.1.0","resolved":"https://registry.example/example/-/example-1.1.0.tgz"}}}`), 0644); err != nil {
			t.Fatal(err)
		}
		gittest.Run(t, req.Workspace, "add", ".")
		gittest.Run(t, req.Workspace, "-c", "user.name=fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "update")
		return execution.Outcome{Termination: execution.Exited, ExitCode: 0, Output: piSays("Updated dependencies and committed.")}
	})
	f.checks = func(execution.Request) execution.Outcome {
		return execution.Outcome{Termination: execution.Exited, ExitCode: 1, Output: []byte("tests failed")}
	}
	return f, calls
}

// The stock producer commits workspace files; routine judges the exact diff,
// including application content, before tests. A fake verdict tests delivery
// and gating, not the model's ability to apply the rubric.
func TestAgentUpdatesJudgeEveryChangedFile(t *testing.T) {
	for _, tc := range []struct {
		name, level          string
		workspace, unrelated bool
		verdict              workflow.CheckVerdict
		want                 workflow.StepState
	}{
		{"workspace", "routine", true, false, workflow.VerdictPass, workflow.StatusPass},
		{"major repair", "aggressive", false, false, workflow.VerdictPass, workflow.StatusPass},
		{"unrelated edit", "aggressive", false, true, workflow.VerdictFail, workflow.StatusFail},
		{"unclear", "aggressive", false, true, workflow.VerdictError, workflow.StatusError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := stock.FS.ReadFile("janitor-update.yaml")
			if err != nil {
				t.Fatal(err)
			}
			doc, err := schema.ParseWorkflow(raw)
			if err != nil {
				t.Fatal(err)
			}
			var rules workflow.StandingRules
			ports := &agentUpdateStanding{doc: doc}
			f, _ := newNodeFixFixture(t, "janitor-update", doc.Spec, func(deps *workflow.Dependencies) { deps.Standing = &rules; deps.StandingAcceptance = ports })
			ports.project = f.projectID
			directory := identity.New(f.db, identity.Config{Projects: project.NewCatalogue(f.db)})
			if _, err := directory.Grant(f.ctx, identity.Spec{SubjectType: "user", Subject: taskAppTownAdministrator, Role: access.RoleProjectApprover, Scope: access.ScopeProject, ScopeID: f.projectID.String(), By: taskAppTownAdministrator}, ""); err != nil {
				t.Fatal(err)
			}
			rules = workflow.NewStandingRules(f.db, workflow.StandingDependencies{Identity: directory, Authority: access.Checked{Authority: directory, Projects: project.New(f.db, directory)}, Procedures: ports, Publications: ports, Portfolio: ports, Presence: ports, Inspector: verification.NewRoutineInspector(f.dir, &scm.RepositoryLocks{}, verification.Executor{}).WithFixes(f.db, adoption.New(f.db, adoption.Dependencies{Ways: f.ways}))})
			definition := workflow.StandingDefinition{Scope: "instance", Delivery: "full", Categories: workflow.StandingCategories{Dependencies: tc.level}}
			person, err := directory.EnsurePerson(f.ctx, taskAppTownAdministrator)
			if err != nil {
				t.Fatal(err)
			}
			human := access.WithCaller(f.ctx, access.Caller{Verified: true, Principal: access.Principal{ID: person.PrincipalID, Kind: "user", Name: taskAppTownAdministrator, Via: "session"}})
			question, err := rules.Preview(human, definition)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := rules.Enable(human, workflow.StandingCommand{Definition: question.Definition, ShownDigest: question.Digest}); err != nil {
				t.Fatal(err)
			}
			answerCheck := func(task *workflow.Execution) {
				if _, err := task.Claim(); err == nil {
					task.Settle((workflow.CheckEvidence{Checks: []workflow.CheckReceipt{{Check: task.Step().Name, Verdict: workflow.VerdictPass}}}).Outcome(workflow.StatusPass, nil))
				}
			}
			f.scan, f.preview = answerCheck, answerCheck
			registry, published := npmRegistry(t)

			f.bound = tc.level
			if tc.workspace {
				updateWrite(t, f.hub, "package.json", `{"name":"root","version":"1.0.0","workspaces":["packages/*"]}`)
				updateWrite(t, f.hub, "packages/a/package.json", `{"name":"a","version":"1.0.0","dependencies":{"example":"^1.0.0"}}`)
			}
			if !tc.workspace {
				updateWrite(t, f.hub, "package.json", `{"name":"app","version":"1.0.0","dependencies":{"example":"^1.0.0"}}`)
				updateWrite(t, f.hub, "src/use.js", "const { original } = require('example'); original();\n")
				updateWrite(t, f.hub, "src/report.js", "exports.reportTitle = 'Report';\n")
			}
			testBody := "require('./src/use.js');\n"
			if tc.workspace {
				testBody = "require('example').original();\n"
			}
			updateWrite(t, f.hub, "test.js", testBody)
			if err := os.Remove(filepath.Join(f.hub, "package-lock.json")); err != nil {
				t.Fatal(err)
			}
			env := []string{"npm_config_registry=" + registry, "npm_config_audit=false", "npm_config_fund=false", "npm_config_cache=" + t.TempDir()}
			if out, code := fixtureShell(t, f.hub, []string{"npm", "install", "--package-lock-only", "--ignore-scripts"}, env); code != 0 {
				t.Fatalf("base lock: %d %s", code, out)
			}
			updateCommit(t, f.hub, "listing-compatible dependencies")
			published.Store(true)
			installFakePi(f, func(_ int, req execution.Request) execution.Outcome {
				manifest := "package.json"
				if tc.workspace {
					manifest = "packages/a/package.json"
				}
				version := "2.0.0"
				if tc.workspace {
					version = "1.0.1"
				}
				name := "app"
				if tc.workspace {
					name = "a"
				}
				updateWrite(t, req.Workspace, manifest, fmt.Sprintf(`{"name":%q,"version":"1.0.0","dependencies":{"example":"^%s"}}`, name, version))
				env := []string{"npm_config_registry=" + registry, "npm_config_audit=false", "npm_config_fund=false", "npm_config_cache=" + t.TempDir()}
				if out, code := fixtureShell(t, req.Workspace, []string{"npm", "install", "--package-lock-only", "--ignore-scripts"}, env); code != 0 {
					t.Fatalf("candidate lock: %d %s", code, out)
				}
				if !tc.workspace {
					updateWrite(t, req.Workspace, "src/use.js", "const { renamed } = require('example'); renamed();\n")
				}
				if tc.unrelated {
					updateWrite(t, req.Workspace, "src/report.js", "exports.newFeature = true;\n")
				}
				updateCommit(t, req.Workspace, "update and required repair")
				return execution.Outcome{Termination: execution.Exited, Output: piSays("committed")}
			})
			called := 0
			f.commands.judge = updateJudgeFunc(func(ctx context.Context, source workflow.Source, item check.Item, diff string) (workflow.CheckReceipt, error) {
				called++
				var shipped string
				for _, step := range doc.Spec.Stages[0].Steps {
					if step.Name == "routine" {
						shipped = step.With["rubric"].(string)
					}
				}
				if !strings.HasPrefix(item.Rubric, shipped+"\n\nApproved level: "+tc.level) {
					t.Fatalf("rubric or level lost: %s", item.Rubric)
				}
				manifest := "package.json"
				if tc.workspace {
					manifest = "packages/a/package.json"
				}
				if !strings.Contains(item.Rubric, manifest) || !strings.Contains(item.Rubric, "package-lock.json") {
					t.Fatal(item.Rubric)
				}
				if tc.unrelated && !strings.Contains(item.Rubric, "other: src/report.js, src/use.js") {
					t.Fatal(item.Rubric)
				}
				base := strings.TrimSpace(gittest.Run(t, item.Repository, "merge-base", "main", item.Commit))
				expected, err := gitexec.Run(ctx, item.Repository, "diff", base+".."+item.Commit)
				if err != nil {
					t.Fatal(err)
				}
				if diff != expected || item.Hash != check.BundleHash(expected) {
					t.Fatal("judge did not receive byte-equal complete diff")
				}
				if item.Namespace != "janitor-update" || item.Version != 1 || item.Name != "routine" {
					t.Fatalf("identity: %+v", item.Declared)
				}
				return workflow.CheckReceipt{Verdict: tc.verdict, Detail: "model reasoning", Evidence: "src/report.js", AgentEvidence: &check.AgentEvidence{}}, nil
			})
			f.checks = func(req execution.Request) execution.Outcome {
				if !strings.Contains(strings.Join(req.Argv, " "), "node test.js") {
					t.Fatalf("declared tests not run: %v", req.Argv)
				}
				if out, code := fixtureShell(t, req.Workspace, []string{"npm", "ci", "--ignore-scripts"}, env); code != 0 {
					t.Fatalf("candidate install: %d %s", code, out)
				}
				if out, code := fixtureShell(t, req.Workspace, []string{"node", "test.js"}, env); code != 0 {
					t.Fatalf("declared tests: %d %s", code, out)
				}
				return execution.Outcome{Termination: execution.Exited}
			}
			run := queueRun(t, f, "janitor-update", "updates")
			steps := queueSteps(t, f, run)
			queueExecute(t, f, steps["update"])
			if status, ev := stepStatus(t, f, steps["update"].ID); status != workflow.StatusPass {
				t.Fatalf("producer %s: %+v", status, ev)
			}
			queueExecute(t, f, steps["routine"])
			status, ev := stepStatus(t, f, steps["routine"].ID)
			if status != tc.want || called != 1 {
				t.Fatalf("routine %s (%d calls): %+v", status, called, ev)
			}
			var facts workflow.CheckEvidence
			current, _ := workflow.ReadStepIn(t.Context(), f.db, steps["routine"].ID)
			if err := json.Unmarshal(current.Evidence, &facts); err != nil {
				t.Fatal(err)
			}
			if len(facts.Checks) != 1 || facts.Checks[0].Met() != (tc.want == workflow.StatusPass) {
				t.Fatalf("receipt: %+v", facts)
			}
			attempts, err := f.work.Attempts(f.ctx, f.projectID, "updates", 10)
			if err != nil || len(attempts) != 1 || len(attempts[0].Files) == 0 {
				t.Fatalf("files: %+v %v", attempts, err)
			}
			if tc.want != workflow.StatusPass {
				_, _ = f.work.Advance(f.ctx, run.ID)
				repair := queueSteps(t, f, run)["repair"]
				if (repair.Status == workflow.StatusPending) != (tc.want == workflow.StatusFail) {
					t.Fatalf("wrong rework for %s: %+v", tc.want, repair)
				}
			}
			if tc.want == workflow.StatusPass {
				queueExecute(t, f, steps["tests"])
				if status, ev := stepStatus(t, f, steps["tests"].ID); status != workflow.StatusPass {
					t.Fatalf("tests %s: %+v", status, ev)
				}
			}
			if tc.want == workflow.StatusPass {
				for _, step := range queueSteps(t, f, run) {
					if step.Type == "scan" || step.Type == "preview" {
						queueExecute(t, f, step)
					}
				}
				_, _ = f.work.Advance(f.ctx, run.ID)
				if len(ports.keys) == 0 {
					t.Fatal("stock update did not reach standing acceptance")
				}
				result, err := f.work.RunStandingAcceptance(t.Context(), ports.keys[len(ports.keys)-1], nil)
				if err != nil || result["cast"] != true {
					t.Fatalf("standing acceptance: %+v %v", result, err)
				}
			} else {
				current, err := f.work.Run(f.ctx, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				result, err := f.work.RunStandingAcceptance(t.Context(), fmt.Sprintf("%s/%s/%d", run.ID, steps["accept"].ID, current.RetryRevision), nil)
				if err != nil || result["cast"] != false {
					t.Fatalf("non-pass got standing acceptance: %+v %v", result, err)
				}
			}

		})
	}
}

func updateWrite(t *testing.T, repo, name, body string) {
	t.Helper()
	path := filepath.Join(repo, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}
func updateCommit(t *testing.T, repo, message string) {
	t.Helper()
	gittest.Run(t, repo, "add", ".")
	gittest.Run(t, repo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", message)
}

func TestRoutineProtectsInputsBeforeJudging(t *testing.T) {
	raw, _ := stock.FS.ReadFile("janitor-update.yaml")
	doc, err := schema.ParseWorkflow(raw)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := newNodeFixFixture(t, "updates", doc.Spec)
	installFakePi(f, func(_ int, req execution.Request) execution.Outcome {
		updateWrite(t, req.Workspace, "mise.toml", "[tools]\nnode='26'\n")
		updateCommit(t, req.Workspace, "toolchain change")
		return execution.Outcome{Termination: execution.Exited, Output: piSays("committed")}
	})
	f.commands.judge = updateJudgeFunc(func(context.Context, workflow.Source, check.Item, string) (workflow.CheckReceipt, error) {
		t.Error("judge saw a protected input change")
		return workflow.CheckReceipt{}, nil
	})
	run := queueRun(t, f, "updates", "updates")
	steps := queueSteps(t, f, run)
	queueExecute(t, f, steps["update"])
	queueExecute(t, f, steps["routine"])
	if status, ev := stepStatus(t, f, steps["routine"].ID); status != workflow.StatusFail || !strings.Contains(fmt.Sprint(ev["reason"]), "toolchain") {
		t.Fatalf("protected inputs: %s %+v", status, ev)
	}
}

func TestRepairRejudgesTheDiffAndKeepsTheInitialProposal(t *testing.T) {
	raw, _ := stock.FS.ReadFile("janitor-update.yaml")
	doc, err := schema.ParseWorkflow(raw)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := newNodeFixFixture(t, "updates", doc.Spec)
	judged := []string{}
	f.commands.judge = updateJudgeFunc(func(_ context.Context, _ workflow.Source, _ check.Item, diff string) (workflow.CheckReceipt, error) {
		judged = append(judged, diff)
		return workflow.CheckReceipt{Verdict: workflow.VerdictPass, Detail: "scope okay"}, nil
	})
	installFakePi(f, func(n int, req execution.Request) execution.Outcome {
		if n == 1 {
			updateWrite(t, req.Workspace, "package-lock.json", `{"version":"v2026.6"}`)
		} else {
			updateWrite(t, req.Workspace, "src/use.js", "required repair\n")
		}
		updateCommit(t, req.Workspace, "update or repair")
		piKeepsSession(t, req, "committed")
		return execution.Outcome{Termination: execution.Exited, Output: piSays("committed")}
	})
	run := queueRun(t, f, "updates", "updates")
	steps := queueSteps(t, f, run)
	queueExecute(t, f, steps["update"])
	queueExecute(t, f, steps["routine"])
	attempts, err := f.work.Attempts(f.ctx, f.projectID, "updates", 10)
	if err != nil {
		t.Fatal(err)
	}
	initial := attempts[0].Fingerprint
	queueExecute(t, f, steps["tests"])
	_, _ = f.work.Advance(f.ctx, run.ID)
	steps = queueSteps(t, f, run)
	if steps["repair"].Status != workflow.StatusPending {
		t.Fatalf("test failure did not select repair: %+v", steps["repair"])
	}
	queueExecute(t, f, steps["repair"])
	_, _ = f.work.Advance(f.ctx, run.ID)
	steps = queueSteps(t, f, run)
	if steps["routine"].Status != workflow.StatusPending {
		t.Fatalf("stale routine receipt survived repair: %+v", steps["routine"])
	}
	queueExecute(t, f, steps["routine"])
	if len(judged) != 2 || judged[0] == judged[1] || !strings.Contains(judged[1], "src/use.js") {
		t.Fatalf("diff not rejudged: %+v", judged)
	}
	attempts, err = f.work.Attempts(f.ctx, f.projectID, "updates", 10)
	if err != nil || attempts[0].Fingerprint != initial {
		t.Fatalf("repair replaced initial proposal: %+v %v", attempts, err)
	}
}

func TestPinnedDependencyFixErrorsAndRetainsItsAttempt(t *testing.T) {
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "update", Steps: []schema.Step{{Name: "update", Uses: FixKind, With: map[string]any{"kind": "update"}}}}}}
	f, _ := newNodeFixFixture(t, "old-update", spec)
	run := queueRun(t, f, "old-update", "updates")
	step := queueSteps(t, f, run)["update"]
	queueExecute(t, f, step)
	status, ev := stepStatus(t, f, step.ID)
	if status != workflow.StatusError || ev["reason"] != "this pack version's dependency update is no longer supported; adopt the current platform pack" {
		t.Fatalf("pinned fix: %s %+v", status, ev)
	}
	attempts, err := f.work.Attempts(f.ctx, f.projectID, "updates", 10)
	if err != nil || len(attempts) != 1 || !attempts[0].Attempted || !attempts[0].Failed {
		t.Fatalf("lost attempted failure: %+v %v", attempts, err)
	}
}

// Only external observations and the job request are faked here. The existing
// task fixture supplies Work, its gates, the agent harness, Git and receipts.
type agentUpdateStanding struct {
	doc     *schema.Doc
	project ids.ID
	keys    []string
}

func (p *agentUpdateStanding) EnqueueStandingAcceptanceIn(_ context.Context, _ *database.Tx, key string) error {
	p.keys = append(p.keys, key)
	return nil
}
func (p *agentUpdateStanding) StandingProcedure(_ context.Context, _ database.Queryer, _ ids.ID, name string) (workflow.StandingProcedure, error) {
	var means string
	for _, step := range p.doc.Spec.Stages[0].Steps {
		if step.Name == "accept" {
			means = step.With["standing"].(map[string]any)["means"].(string)
		}
	}
	return workflow.StandingProcedure{Acceptance: workflow.StandingPurpose{Workflow: name, Version: 1, Step: "accept", Means: means, Approvers: []string{"role:project-approver"}}, Landing: workflow.StandingPurpose{Workflow: "land", Version: 1, Step: "accept", Means: "land the checked candidate", Approvers: []string{"role:project-approver"}}}, nil
}
func (p *agentUpdateStanding) InstanceStandingProcedure(name string, _ []*adoption.ResolvedWorkflow) (workflow.StandingProcedure, error) {
	return p.StandingProcedure(context.Background(), nil, p.project, name)
}
func (p *agentUpdateStanding) PublishedWorkflows(context.Context, database.Queryer) ([]*adoption.ResolvedWorkflow, error) {
	return []*adoption.ResolvedWorkflow{{Name: p.doc.Metadata.Name, Version: 1, Doc: p.doc}}, nil
}
func (p *agentUpdateStanding) StandingPortfolio(context.Context, database.Queryer, ids.ID, workflow.StandingCategories) ([]workflow.StandingProjectPreview, error) {
	return []workflow.StandingProjectPreview{{ProjectID: p.project, Deployments: []workflow.StandingEnvironment{}}}, nil
}
func (p *agentUpdateStanding) StandingHold(context.Context, database.Queryer, ids.ID) (string, error) {
	return "", nil
}
func (p *agentUpdateStanding) Gone(context.Context, string) (bool, error) {
	return false, fmt.Errorf("inspection cannot remove chats")
}
func (p *agentUpdateStanding) ChatEnded(context.Context, string) (bool, error) { return true, nil }
func (p *agentUpdateStanding) UnsettledRuns(context.Context, database.Queryer) ([]ids.ID, error) {
	return nil, nil
}
func (p *agentUpdateStanding) FolderClear(context.Context, ids.ID) (bool, error) {
	return false, fmt.Errorf("inspection cannot remove runtimes")
}
func (p *agentUpdateStanding) FolderUnwritten(context.Context, ids.ID) (bool, error) {
	return true, nil
}

func TestAgentUpdateDoesNotRequireAnEcosystemDeclarationOrSemver(t *testing.T) {
	raw, _ := stock.FS.ReadFile("janitor-update.yaml")
	doc, err := schema.ParseWorkflow(raw)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := newNodeFixFixture(t, "updates", doc.Spec)
	updateWrite(t, f.hub, "composer.json", `{"require":{"example/library":"v2026.6"}}`)
	updateCommit(t, f.hub, "undeclared ecosystem")
	installFakePi(f, func(_ int, req execution.Request) execution.Outcome {
		updateWrite(t, req.Workspace, "composer.json", `{"require":{"example/library":"v2026.7"}}`)
		updateCommit(t, req.Workspace, "calendar version update")
		return execution.Outcome{Termination: execution.Exited, Output: piSays("committed")}
	})
	called := false
	f.commands.judge = updateJudgeFunc(func(_ context.Context, _ workflow.Source, item check.Item, diff string) (workflow.CheckReceipt, error) {
		called = true
		if !strings.Contains(item.Rubric, "other: composer.json") || !strings.Contains(diff, "v2026.7") {
			t.Fatalf("undeclared changes not judged: %s\n%s", item.Rubric, diff)
		}
		return workflow.CheckReceipt{Verdict: workflow.VerdictPass, Detail: "calendar version update within scope"}, nil
	})
	run := queueRun(t, f, "updates", "updates")
	steps := queueSteps(t, f, run)
	queueExecute(t, f, steps["update"])
	queueExecute(t, f, steps["routine"])
	if status, ev := stepStatus(t, f, steps["routine"].ID); status != workflow.StatusPass || !called {
		t.Fatalf("undeclared ecosystem refused: %s %+v", status, ev)
	}
}
