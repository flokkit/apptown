package verification

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	piharness "github.com/flokkit/apptown/internal/apptown/agentharness/pi"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/inference"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
)

type fixtureModels struct{}

func (fixtureModels) ModelsJSON(context.Context) ([]byte, error) {
	return []byte(`{"providers":{}}`), nil
}

// fixtureCheckAgent is a task-side harness driven by a fake model. The model
// asks to inspect the root, then sees that tool result and issues one verdict.
// It exercises the real gateway and its token/spend accounting, without a paid
// model or Docker. Its requested file lookup is retained as an observation.
type fixtureCheckAgent struct {
	executionfake.Tasks
	t           *testing.T
	db          *database.DB
	specs       map[string]execution.TaskSpec
	gateway     *inference.Gateway
	starts      int
	tokens      int64
	block       bool
	fail        bool
	unclear     bool
	lastToken   string
	refuseOnce  bool
	noInference bool
}

func (d *fixtureCheckAgent) Ensure(ctx context.Context, s execution.TaskSpec) (execution.TaskObservation, error) {
	if d.specs == nil {
		d.specs = map[string]execution.TaskSpec{}
	}
	d.specs[s.Key] = s
	fake := s
	fake.Results = nil
	if d.block {
		fake.Env = append(fake.Env, "FAKE_BLOCK=1")
	}
	if d.fail {
		fake.Env = append(fake.Env, "FAKE_EXIT=1")
	}
	return d.Tasks.Ensure(ctx, fake)
}
func (d *fixtureCheckAgent) Start(ctx context.Context, ref execution.TaskRef) error {
	d.starts++
	if d.noInference {
		return d.Tasks.Start(ctx, ref)
	}
	s := d.specs[ref.Key]
	if s.ReadOnlySource == "" || s.Workspace == "" {
		return fmt.Errorf("check has no separate source and work")
	}
	var token string
	modelPattern := "fixture/judge"
	for i, arg := range s.Argv {
		if arg == "--model" && i+1 < len(s.Argv) {
			modelPattern = s.Argv[i+1]
		}
	}
	for _, v := range s.Env {
		if strings.HasPrefix(v, "APPTOWN_SESSION_TOKEN=") {
			token = strings.TrimPrefix(v, "APPTOWN_SESSION_TOKEN=")
		}
	}
	d.lastToken = token
	ask := func(message string) (string, error) {
		raw, _ := json.Marshal(map[string]any{"model": strings.TrimPrefix(modelPattern, "fixture/"), "messages": []map[string]string{{"role": "user", "content": message}}})
		r := httptest.NewRequest("POST", "/providers/fixture/chat/completions", strings.NewReader(string(raw)))
		r.RemoteAddr = "192.0.2.10:12"
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		d.gateway.ServeHTTP(w, r)
		if w.Code != 200 {
			return "", fmt.Errorf("gateway %d: %s", w.Code, w.Body.String())
		}
		var out struct {
			Choices []struct{ Message struct{ Content string } }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			return "", err
		}
		return out.Choices[0].Message.Content, nil
	}
	tool, err := ask("inspect root")
	if err != nil && d.refuseOnce {
		tool, err = ask("inspect root")
	}
	if err != nil {
		return err
	}
	if tool != "lookup mise.toml" {
		return fmt.Errorf("fake model did not request its file lookup: %s", tool)
	}
	_, statErr := os.Stat(filepath.Join(s.ReadOnlySource, "mise.toml"))
	observed := "absent"
	if statErr == nil {
		observed = "present"
	}
	answer, err := ask(observed)
	if err != nil {
		return err
	}
	if d.unclear {
		answer = `{"verdict":"unclear","note":"cannot decide"}`
	}
	dir := filepath.Join(s.Workspace, ".apptown")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	observation, _ := json.Marshal(map[string]string{"tool": "lookup", "path": "/src/mise.toml", "result": observed})
	transcript := append(piharness.SessionHeader(strings.TrimPrefix(s.Key, "check-"), time.Now(), "/work"), observation...)
	transcript = append(transcript, '\n')
	for name, body := range map[string]string{"check.json": answer, "session.jsonl": string(transcript), "events.jsonl": string(observation) + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			return err
		}
	}
	return d.Tasks.Start(ctx, ref)
}

func fixtureCheckJudge(t *testing.T) (*CheckJudge, *fixtureCheckAgent, ids.ID) {
	t.Helper()
	f := newScannerOwnerFixture(t)
	d := &fixtureCheckAgent{t: t, tokens: 120, db: f.DB}
	refused := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			http.NotFound(w, r)
			return
		}
		if d.refuseOnce && !refused {
			refused = true
			w.WriteHeader(529)
			return
		}
		var request struct{ Messages []struct{ Content string } }
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &request); err != nil {
			t.Error(err)
		}
		content := "lookup mise.toml"
		if len(request.Messages) > 0 {
			switch request.Messages[0].Content {
			case "present":
				content = `{"verdict":"pass","evidence":"/src/mise.toml; ls -a /src","note":"root toolchain file exists"}`
			case "absent":
				content = `{"verdict":"fail","evidence":"/src/mise.toml absent; ls -a /src","note":"root toolchain file is absent"}`
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture-served", "choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}}, "usage": map[string]any{"prompt_tokens": d.tokens, "completion_tokens": 40, "cost": 0.001}})
	}))
	t.Cleanup(upstream.Close)
	cfg, _ := json.Marshal(map[string]any{"baseUrl": upstream.URL, "api": "openai-completions", "models": []string{"judge", "other"}})
	if _, err := integration.NewWithClock(f.DB, nil).Save(t.Context(), "fixture", "model", "fixture", string(cfg), "fixture"); err != nil {
		t.Fatal(err)
	}
	sessions := &inference.CheckSessions{}
	d.gateway = inference.New([]netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, inference.Identity{Checks: sessions}, inference.NewCatalog(f.DB, secret.NewService(f.DB, secret.Config{DataDir: t.TempDir()})), usage.NewRequests(f.DB))
	j := &CheckJudge{Driver: d, Image: func(context.Context) (string, error) { return "fixture-agent@immutable", nil }, Models: fixtureModels{}, Sessions: sessions, Capacity: model.NewScheduler(nil, 1), Directory: t.TempDir(), Profile: "fixture", TimeLimit: time.Minute, TokenLimit: 100000}
	return j, d, f.ProjectID
}
func fixtureRubric() check.Declared {
	return check.Declared{Namespace: "tools", Version: 1, Declaration: check.Declaration{AppliesTo: []string{"repository"}, Name: "root-toolchain", Evaluator: "llm", Scope: check.ScopeTree, Files: []string{"README.md"}, Rubric: "a root mise.toml exists"}}
}
func fixtureInvocation(t *testing.T, pid ids.ID) checkInvocation {
	t.Helper()
	repo := t.TempDir()
	commits := gittest.BareRepository(t, repo, "main", gittest.Commit{Message: "base", Files: map[string]string{"README.md": "app", "elsewhere.txt": strings.Repeat("large unrelated source\n", 10000)}}, gittest.Commit{Message: "toolchain", Files: map[string]string{"mise.toml": "[tools]\nnode='24'\n"}})
	return checkInvocation{Repository: repo, Commit: commits[1], Rubric: fixtureRubric(), Call: modelcall.Call{ProjectID: pid, Model: "fixture/judge"}}
}

func TestAgentChecksExploreTheCheckoutAndReuseExactInputs(t *testing.T) {
	j, d, pid := fixtureCheckJudge(t)
	in := fixtureInvocation(t, pid)
	first := j.JudgeCheck(t.Context(), check.Item{Repository: in.Repository, Commit: in.Commit, Declared: in.Rubric, Recheck: in.Recheck}, in.Call, in.Diff)
	if first.Reason != "" || first.Verdict.Verdict != "pass" || first.Evidence.ServedModel != "fixture-served" || first.Evidence.TokensIn != 240 {
		t.Fatalf("checkout verdict: %+v", first)
	}
	if again := j.JudgeCheck(t.Context(), check.Item{Repository: in.Repository, Commit: in.Commit, Declared: in.Rubric, Recheck: in.Recheck}, in.Call, in.Diff); !again.Evidence.Reused || d.starts != 1 {
		t.Fatalf("unchanged inputs did not reuse: %+v; sessions %d", again, d.starts)
	}
	// A different commit with the same tree reuses while retaining the
	// original session's actual source commit in its evidence.
	tree := gittest.Run(t, in.Repository, "rev-parse", in.Commit+"^{tree}")
	same := in
	same.Commit = gittest.Run(t, in.Repository, "commit-tree", tree, "-p", in.Commit, "-m", "same tree")
	if r := j.JudgeCheck(t.Context(), check.Item{Repository: same.Repository, Commit: same.Commit, Declared: same.Rubric, Recheck: same.Recheck}, same.Call, same.Diff); !r.Evidence.Reused || r.Evidence.Commit != in.Commit {
		t.Fatalf("identical tree changed source provenance: %+v", r)
	}
	changed := in
	changed.Rubric.Version++
	if r := j.JudgeCheck(t.Context(), check.Item{Repository: changed.Repository, Commit: changed.Commit, Declared: changed.Rubric, Recheck: changed.Recheck}, changed.Call, changed.Diff); r.Evidence.Reused || d.starts != 2 {
		t.Fatalf("adopted version reused: %+v", r)
	}
	changed = in
	changed.Rubric.Declaration.Rubric += " explicitly"
	_ = j.JudgeCheck(t.Context(), check.Item{Repository: changed.Repository, Commit: changed.Commit, Declared: changed.Rubric, Recheck: changed.Recheck}, changed.Call, changed.Diff)
	if d.starts != 3 {
		t.Fatal("changed rubric reused")
	}
	changed = in
	changed.Rubric.EvidenceUse = "wording"
	if r := j.JudgeCheck(t.Context(), check.Item{Repository: changed.Repository, Commit: changed.Commit, Declared: changed.Rubric, Recheck: changed.Recheck}, changed.Call, changed.Diff); r.Evidence.Reused || d.starts != 4 {
		t.Fatalf("changed evidence use reused: %+v; sessions %d", r, d.starts)
	}
	wordingPrompt := false
	for _, spec := range d.specs {
		prompt := strings.Join(spec.Argv, "\n")
		if !strings.Contains(prompt, "PROJECT-NOTES.md and AGENTS.md describe intent and claims") || !strings.Contains(prompt, "Cite independent code, tests or observed results") {
			t.Fatalf("check prompt omitted claims rule: %s", prompt)
		}
		if strings.Contains(prompt, "evidenceUse=wording\nSelector hints:") {
			wordingPrompt = true
		}
	}
	if !wordingPrompt {
		t.Fatal("check prompt omitted declared evidenceUse=wording")
	}
	changed = in
	changed.Recheck = true
	_ = j.JudgeCheck(t.Context(), check.Item{Repository: changed.Repository, Commit: changed.Commit, Declared: changed.Rubric, Recheck: changed.Recheck}, changed.Call, changed.Diff)
	if d.starts != 5 {
		t.Fatal("Re-check reused")
	}
	changed = in
	changed.Commit = gittest.Run(t, in.Repository, "rev-parse", in.Commit+"^")
	r := j.JudgeCheck(t.Context(), check.Item{Repository: changed.Repository, Commit: changed.Commit, Declared: changed.Rubric, Recheck: changed.Recheck}, changed.Call, changed.Diff)
	if r.Verdict.Verdict != "fail" || r.Evidence.Reused {
		t.Fatalf("absent file in another tree: %+v", r)
	}
	body, err := os.ReadFile(filepath.Join(j.Directory, "sessions", r.Evidence.Session+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Transcript") || strings.Contains(string(body), `"filesRead"`) {
		t.Fatalf("session should retain transcript, with no complete file list: %s", body)
	}
}

func TestDiffChecksReuseOnlyTheExactTransition(t *testing.T) {
	j, d, pid := fixtureCheckJudge(t)
	in := fixtureInvocation(t, pid)
	in.Rubric.Scope = check.ScopeDiff
	repo := t.TempDir()
	commits := gittest.BareRepository(t, repo, "main", gittest.Commit{Message: "first base", Files: map[string]string{"README.md": "first base"}}, gittest.Commit{Message: "second base", Files: map[string]string{"README.md": "second base"}}, gittest.Commit{Message: "candidate", Files: map[string]string{"mise.toml": "[tools]\nnode='24'\n"}})
	in.Repository, in.Commit = repo, commits[2]
	in.Diff = gittest.Run(t, repo, "diff", commits[0], in.Commit)
	_ = j.JudgeCheck(t.Context(), check.Item{Repository: in.Repository, Commit: in.Commit, Declared: in.Rubric, Recheck: in.Recheck}, in.Call, in.Diff)
	// The candidate stays fixed while its comparison base changes.
	in.Diff = gittest.Run(t, repo, "diff", commits[1], in.Commit)
	r := j.JudgeCheck(t.Context(), check.Item{Repository: in.Repository, Commit: in.Commit, Declared: in.Rubric, Recheck: in.Recheck}, in.Call, in.Diff)
	if d.starts != 2 || r.Evidence.Reused {
		t.Fatal("different supplied diff reused a verdict")
	}
	if r = j.JudgeCheck(t.Context(), check.Item{Repository: in.Repository, Commit: in.Commit, Declared: in.Rubric, Recheck: in.Recheck}, in.Call, in.Diff); !r.Evidence.Reused || d.starts != 2 {
		t.Fatal("same transition did not reuse")
	}
}

// checkDeadlineDriver advances the owner's clock only at the selected task
// boundary. Setup and inference cannot consume the cap under machine load.
type checkDeadlineDriver struct {
	execution.TaskDriver
	expire func()
	phase  string
}

func (d checkDeadlineDriver) Ensure(ctx context.Context, spec execution.TaskSpec) (execution.TaskObservation, error) {
	if d.phase == "ensure" {
		d.expire()
		return execution.TaskObservation{}, fmt.Errorf("backend refused task")
	}
	return d.TaskDriver.Ensure(ctx, spec)
}

func (d checkDeadlineDriver) Start(ctx context.Context, ref execution.TaskRef) error {
	if d.phase == "start" {
		d.expire()
		return fmt.Errorf("backend refused start")
	}
	return d.TaskDriver.Start(ctx, ref)
}

func (d checkDeadlineDriver) Wait(context.Context, execution.TaskRef) (int, error) {
	d.expire()
	// Deliberately return a different failure at the cap: the owner's clock
	// must determine the timeout, independently of the backend's error.
	return 1, fmt.Errorf("backend disconnected")
}

func fixtureCheckDeadline(j *CheckJudge, d *fixtureCheckAgent, phase string) {
	// Keep the OS context deadline in the future; expiry is judged by the
	// same injected clock in the judge, credentials and task admission.
	now := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	j.Now = func() time.Time { return now }
	j.Sessions.Now = j.Now
	d.Tasks.Now = j.Now
	if phase != "" {
		j.Driver = checkDeadlineDriver{TaskDriver: d, phase: phase, expire: func() { now = now.Add(j.TimeLimit) }}
	}
}

func TestCappedAndFailedCheckSessionsNeverEarnVerdictsOrReuse(t *testing.T) {
	for _, kind := range []string{"tokens", "tokens-before-start", "time", "tokens-and-time", "agent"} {
		t.Run(kind, func(t *testing.T) {
			j, d, pid := fixtureCheckJudge(t)
			in := fixtureInvocation(t, pid)
			switch kind {
			case "tokens-and-time":
				j.TimeLimit = 500 * time.Millisecond
				d.block = true
				fallthrough
			case "tokens":
				j.TokenLimit = 320
				d.fail = kind == "tokens"
			case "tokens-before-start":
				// The first turn exhausts the cap; the next inference request
				// makes Start fail before the backend reaches Wait.
				j.TokenLimit = 160
			case "time":
				j.TimeLimit = 500 * time.Millisecond
				d.block = true
			case "agent":
				d.fail = true
			}
			phase := ""
			if strings.Contains(kind, "time") {
				phase = "wait"
			}
			fixtureCheckDeadline(j, d, phase)
			r := j.JudgeCheck(t.Context(), check.Item{Repository: in.Repository, Commit: in.Commit, Declared: in.Rubric, Recheck: in.Recheck}, in.Call, in.Diff)
			if r.Reason == "" || r.Verdict.Verdict != "" {
				t.Fatalf("failed check earned a verdict: %+v", r)
			}
			if strings.HasPrefix(kind, "tokens") && !strings.Contains(r.Reason, "token limit") {
				t.Fatalf("missing cap reason: %+v", r)
			}
			if kind == "time" && !strings.Contains(r.Reason, "timed out after 500ms") {
				t.Fatalf("missing timeout reason: %+v", r)
			}
			_ = j.JudgeCheck(t.Context(), check.Item{Repository: in.Repository, Commit: in.Commit, Declared: in.Rubric, Recheck: in.Recheck}, in.Call, in.Diff)
			if d.starts != 2 {
				t.Fatal("cannot-assess result reused")
			}
		})
	}
}

// Expiry can coincide with setup failure before any inference was observed.
// Neither the backend error nor the empty usage report may mask the cap.
func TestCheckTimeoutDuringTaskSetupKeepsTimeoutReason(t *testing.T) {
	for _, phase := range []string{"ensure", "start", "wait"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			j, d, pid := fixtureCheckJudge(t)
			in := fixtureInvocation(t, pid)
			d.noInference = true
			d.block = true
			fixtureCheckDeadline(j, d, phase)
			for range 2 {
				r := j.JudgeCheck(t.Context(), check.Item{Repository: in.Repository, Commit: in.Commit, Declared: in.Rubric, Recheck: in.Recheck}, in.Call, in.Diff)
				if r.Reason != "check did not finish: timed out after 1m0s" || r.Verdict.Verdict != "" || r.Evidence.Reused || r.Evidence.AppTownFault {
					t.Fatalf("timeout masked by %s failure: %+v", phase, r)
				}
			}
		})
	}
}

func TestGuidanceAndFleetInvokeOneAgentSessionPerCheck(t *testing.T) {
	j, d, pid := fixtureCheckJudge(t)
	in := fixtureInvocation(t, pid)
	e := NewEvaluator(nil, nil, stock.Versions()).WithCheckJudge(j)
	checks := []conformance.DeclaredCheck{{Block: in.Rubric.Namespace, Version: in.Rubric.Version, Check: in.Rubric.Declaration}, {Block: "tools", Version: 1, Check: in.Rubric.Declaration}}
	checks[1].Check.Name = "second-check"
	f := Fleet{evaluator: e}
	got := f.assessChecks(t.Context(), checks, assessInput{repository: in.Repository, tool: toolContext{SHA: in.Commit}, judge: func(items []check.Item, diff string) (map[int]check.Verdict, *llmMetadata, error) {
		return e.judgeChecks(t.Context(), in.Call, items, diff)
	}})
	if len(got) != 2 || d.starts != 2 || got[0].State != conformance.Conforms {
		t.Fatalf("fleet sessions=%d results=%+v", d.starts, got)
	}
	a := Guidance{evaluator: e, selection: selectionWithDefault(t, d.db, "fixture/judge")}
	receipts, _, _ := a.runLLMChecklist(t.Context(), workflow.Source{ProjectID: pid}, checks, checked{Repo: in.Repository, Commit: in.Commit})
	if len(receipts) != 2 || d.starts != 2 || receipts[0].AgentEvidence == nil || !receipts[0].AgentEvidence.Reused {
		t.Fatalf("guidance did not reuse the exact fleet input: %+v; sessions=%d", receipts, d.starts)
	}
}

func TestChangedModelRerunsAndUnclearVerdictsCanReuse(t *testing.T) {
	j, d, pid := fixtureCheckJudge(t)
	in := fixtureInvocation(t, pid)
	_ = j.JudgeCheck(t.Context(), check.Item{Repository: in.Repository, Commit: in.Commit, Declared: in.Rubric, Recheck: in.Recheck}, in.Call, in.Diff)
	in.Call.Model = "fixture/other"
	if r := j.JudgeCheck(t.Context(), check.Item{Repository: in.Repository, Commit: in.Commit, Declared: in.Rubric, Recheck: in.Recheck}, in.Call, in.Diff); r.Reason != "" || r.Evidence.Reused || d.starts != 2 {
		t.Fatalf("new model did not rerun: %+v; sessions %d", r, d.starts)
	}
	d.unclear = true
	in.Rubric.Name = "unclear-check"
	r := j.JudgeCheck(t.Context(), check.Item{Repository: in.Repository, Commit: in.Commit, Declared: in.Rubric, Recheck: in.Recheck}, in.Call, in.Diff)
	if r.Verdict.Verdict != "unclear" || r.Reason != "" {
		t.Fatalf("unclear verdict: %+v", r)
	}
	if r = j.JudgeCheck(t.Context(), check.Item{Repository: in.Repository, Commit: in.Commit, Declared: in.Rubric, Recheck: in.Recheck}, in.Call, in.Diff); !r.Evidence.Reused || d.starts != 3 {
		t.Fatalf("unclear exact input did not reuse: %+v", r)
	}
}

// An agent/backend failure is recorded but must not turn findings-only AI
// checks into a new approval gate by withholding carried acceptance.
func TestUnavailableGuidanceAgentRecordsAppTownInability(t *testing.T) {
	j, d, pid := fixtureCheckJudge(t)
	in := fixtureInvocation(t, pid)
	d.fail = true
	a := Guidance{evaluator: NewEvaluator(nil, nil, stock.Versions()).WithCheckJudge(j), selection: selectionWithDefault(t, d.db, "fixture/judge")}
	checks := []conformance.DeclaredCheck{{Block: in.Rubric.Namespace, Version: in.Rubric.Version, Check: in.Rubric.Declaration}}
	receipts, _, _ := a.runLLMChecklist(t.Context(), workflow.Source{ProjectID: pid}, checks, checked{Repo: in.Repository, Commit: in.Commit})
	if len(receipts) != 1 || receipts[0].Verdict != workflow.VerdictCannotAssess || !receipts[0].AppTownFault || receipts[0].Gates == nil || *receipts[0].Gates {
		t.Fatalf("unavailable AI became candidate fault: %+v", receipts)
	}
}

func TestRefusedProviderRequestCanRetryAndEarnAVerdict(t *testing.T) {
	t.Parallel()
	j, d, pid := fixtureCheckJudge(t)
	d.refuseOnce = true
	in := fixtureInvocation(t, pid)
	r := j.JudgeCheck(t.Context(), check.Item{Repository: in.Repository, Commit: in.Commit, Declared: in.Rubric, Recheck: in.Recheck}, in.Call, in.Diff)
	if r.Reason != "" || r.Verdict.Verdict != "pass" || r.Evidence.TokensIn != 240 {
		t.Fatalf("retry failed: %+v", r)
	}
}

func TestCheckSetupFailuresRetainOperatorClassification(t *testing.T) {
	for _, kind := range []string{"model", "gateway", "image", "worker"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			j, _, pid := fixtureCheckJudge(t)
			in := fixtureInvocation(t, pid)
			switch kind {
			case "model":
				in.Call.Model = ""
			case "gateway":
				j.Models = brokenCheckModels{}
			case "image":
				j.Image = func(context.Context) (string, error) {
					return "", fmt.Errorf("image missing; an administrator must install the agent image")
				}
			case "worker":
				j.Driver = nil
			}
			r := j.JudgeCheck(t.Context(), check.Item{Repository: in.Repository, Commit: in.Commit, Declared: in.Rubric, Recheck: in.Recheck}, in.Call, in.Diff)
			if r.Reason == "" || !r.Evidence.AppTownFault {
				t.Fatalf("setup failure hidden: %+v", r)
			}
		})
	}
}

type brokenCheckModels struct{}

func (brokenCheckModels) ModelsJSON(context.Context) ([]byte, error) {
	return nil, fmt.Errorf("no private model gateway configured. An administrator must configure the inference listener")
}

func TestGuidanceTaskWithoutInferenceRetainsAppTownInability(t *testing.T) {
	t.Parallel()
	j, d, pid := fixtureCheckJudge(t)
	in := fixtureInvocation(t, pid)
	d.noInference = true
	a := Guidance{evaluator: NewEvaluator(nil, nil, stock.Versions()).WithCheckJudge(j), selection: selectionWithDefault(t, d.db, "fixture/judge")}
	checks := []conformance.DeclaredCheck{{Block: in.Rubric.Namespace, Version: in.Rubric.Version, Check: in.Rubric.Declaration}}
	receipts, _, _ := a.runLLMChecklist(t.Context(), workflow.Source{ProjectID: pid}, checks, checked{Repo: in.Repository, Commit: in.Commit})
	if len(receipts) != 1 || !receipts[0].AppTownFault || receipts[0].AgentEvidence == nil || !receipts[0].AgentEvidence.AppTownFault || !strings.Contains(receipts[0].Detail, "no observed inference requests") {
		t.Fatalf("task that never called a provider lost execution failure: %+v", receipts)
	}
}

func TestRoutineUsesTheCheckJudgeAsAGate(t *testing.T) {
	for _, mode := range []string{"pass", "fail", "unclear", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			j, d, pid := fixtureCheckJudge(t)
			in := fixtureInvocation(t, pid)
			d.unclear = mode == "unclear"
			d.fail = mode == "unavailable"
			a := Guidance{evaluator: NewEvaluator(nil, nil, stock.Versions()).WithCheckJudge(j), selection: selectionWithDefault(t, d.db, "fixture/judge")}
			item := check.Item{Repository: in.Repository, Commit: in.Commit, Declared: in.Rubric, Scope: check.ScopeDiff, Hash: check.BundleHash(in.Diff)}
			if mode == "fail" {
				item.Commit = strings.TrimSpace(gittest.Run(t, in.Repository, "rev-parse", in.Commit+"^"))
			}
			receipt, err := a.JudgeUpdate(t.Context(), workflow.Source{ProjectID: pid}, item, in.Diff)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "fail" && (receipt.Verdict != workflow.VerdictFail || receipt.Met()) {
				t.Fatalf("failure met: %+v", receipt)
			}
			if mode == "pass" && !receipt.Met() {
				t.Fatalf("pass: %+v", receipt)
			}
			if (mode == "unclear" || mode == "unavailable") && (receipt.Verdict != workflow.VerdictError || receipt.Met()) {
				t.Fatalf("unknown met: %+v", receipt)
			}
			if receipt.Gates != nil && !*receipt.Gates {
				t.Fatalf("routine inherited calibration: %+v", receipt)
			}
			if receipt.AgentEvidence == nil || receipt.Evaluator != "routine" {
				t.Fatalf("evidence missing: %+v", receipt)
			}
		})
	}
}
