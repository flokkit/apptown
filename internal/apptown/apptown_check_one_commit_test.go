package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

func TestAnAppTownCheckWithAnErrorIsNotOK(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"sh", "tool", "source"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			checks := `[{"appliesTo": ["repository"], "name":"error","evaluator":"sh","sh":"true"}]`
			if kind == "tool" {
				checks = `[{"appliesTo": ["repository"], "name":"error","evaluator":"tool","tool":"missing-binding"}]`
			}
			if kind == "source" {
				checks = `[]`
			}
			a, ch := step1ReviewGuidance(t, true, checks)
			if kind == "source" {
				attachScanner(t, a, "broken-source", map[string]any{"image": "scanner:test", "command": []string{"scan"}, "target": "source", "kind": "secret"})
			}
			installCheckEffects(t, a, checkOverrides{run: func(execRequest) execOutcome { return execOutcome{RunErr: errors.New("review execution failure")} }})
			out, err := a.verification.Check(a.asEngine(context.Background()), ch, verification.Selection{})
			if err != nil {
				t.Fatal(err)
			}
			if out["ok"] != false || out["checkErrors"].(int) < 1 {
				t.Fatalf("error accepted: %v", out)
			}
		})
	}
}

func TestAppTownCheckRefusesAFolderWithoutARepository(t *testing.T) {
	t.Parallel()
	a, ch := step1ReviewGuidance(t, false, `[]`)
	if out, err := a.verification.Check(a.asEngine(context.Background()), ch, verification.Selection{}); err == nil {
		t.Fatalf("no repository accepted: %v", out)
	}
}

func TestAnUnconfirmedSaveRefusesTheCheckOverMCP(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	w.commitOnWorker(t, s.ID, "unsaved.txt", "work")
	token := mcp_token(t, w.a, "unavailable-check", "owner", "operator")
	w.srv.Close()
	result := mcp_call(t, w.a, token, "run_change_checks", map[string]any{"id": w.change.ID})
	if result["isError"] != true || !strings.Contains(mustJSON(result), "was not checked") {
		t.Fatalf("unavailable save allowed: %s", mustJSON(result))
	}
}

func TestAppTownCheckChecksOneCommitWhileTheBranchMoves(t *testing.T) {
	t.Parallel()
	a, ch := step1ReviewGuidance(t, true, `[{"appliesTo": ["repository"], "name":"source","evaluator":"sh","sh":"true"},{"appliesTo": ["repository"], "name":"image","evaluator":"tool","tool":"step1-image","severity":"high"}]`)
	attachScanner(t, a, "step1-image", map[string]any{"command": []string{"true"}, "target": "exec-image", "manual": true})
	ws := (workflow.Layout{DataDir: a.cfg.dataDir}).ChangeWorkspace(ch.ID)
	if err := os.MkdirAll(filepath.Join(ws, ".apptown"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{".apptown/app.yaml": "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n", "source.txt": "first"} {
		if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	gittest.Run(t, ws, "add", "-f", ".apptown/app.yaml", "source.txt")
	gittest.Run(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "first")
	checked := gittest.Run(t, ws, "rev-parse", "HEAD")
	daemon := newDockerFake()
	a.docker.Client = daemon
	moved := false
	installCheckEffects(t, a, checkOverrides{run: func(req execRequest) execOutcome {
		if !moved {
			moved = true
			body, err := os.ReadFile(filepath.Join(req.Workspace, "source.txt"))
			if err != nil || string(body) != "first" {
				t.Fatalf("wrong source %s %v", body, err)
			}
			if err := os.WriteFile(filepath.Join(ws, "source.txt"), []byte("second"), 0644); err != nil {
				t.Fatal(err)
			}
			gittest.Run(t, ws, "add", "source.txt")
			gittest.Run(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "concurrent commit")
		}
		return execOutcome{}
	}})
	out, err := a.verification.Check(a.asEngine(context.Background()), ch, verification.Selection{})
	if err != nil {
		t.Fatal(err)
	}
	calls := fmt.Sprint(daemon.Matching("image inspect "))
	if !moved || out["commit"] != checked || !strings.Contains(calls, "apptown-conform:"+checked[:12]) {
		t.Fatalf("identity drift: %v %s", out, calls)
	}
}

// `apptown check` over AppTown's own HTTP route checks the commit it names
// when the change's folder has it, answering for exactly that commit; a
// commit AppTown does not have is refused naming it, with no result; a
// request naming nothing checks the change's latest commit, as before.
func TestAppTownCheckOverHTTPChecksTheNamedCommitOrRefuses(t *testing.T) {
	t.Parallel()
	a, ch := step1ReviewGuidance(t, true, `[{"appliesTo": ["repository"], "name":"clean","evaluator":"sh","sh":"true"}]`)
	var saw []string // work.txt in each tree a check ran on
	installCheckEffects(t, a, checkOverrides{run: func(req execRequest) execOutcome {
		body, _ := os.ReadFile(filepath.Join(req.Workspace, "work.txt"))
		saw = append(saw, string(body))
		return execOutcome{Termination: execution.Exited}
	}})
	ctx := context.Background()
	if _, err := a.Access.Bootstrap(ctx, identity.NewAccount{Username: "boss", Password: "test-password"}); err != nil {
		t.Fatal(err)
	}
	if err := a.EnsureAuthorization(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Access.EnsureUser(ctx, "owner"); err != nil {
		t.Fatal(err)
	}
	for _, cap := range []access.Capability{access.CapProjectRead, access.CapChangeRunChecks} {
		if _, err := a.Access.Grant(ctx, identity.Spec{SubjectType: "user", Subject: "owner", Capability: string(cap), Scope: access.ScopeProject, ScopeID: fmt.Sprint(ch.ProjectID), By: "boss"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	ws := (workflow.Layout{DataDir: a.cfg.dataDir}).ChangeWorkspace(ch.ID)
	if err := os.WriteFile(filepath.Join(ws, "work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, ws, "add", "work.txt")
	gittest.Run(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "work")
	head := gittest.Run(t, ws, "rev-parse", "HEAD")
	check := func(body string) (int, string, map[string]any) {
		t.Helper()
		resp := authzCall(a.Handler(), "owner", "POST", fmt.Sprintf("/api/v1/changes/%d/check", ch.ID), body)
		var out map[string]any
		_ = json.Unmarshal(resp.Body.Bytes(), &out)
		return resp.Code, resp.Body.String(), out
	}
	if code, body, out := check(`{"commit":"` + head + `"}`); code != 200 || out["commit"] != head || out["ok"] != true || len(saw) == 0 || saw[0] != "work\n" {
		t.Fatalf("the folder's HEAD: %d %s, ran on %q", code, body, saw)
	}
	ran := len(saw) // the change's tree, then its base's
	elsewhere := strings.Repeat("0f", 20)
	code, body, out := check(`{"commit":"` + elsewhere + `"}`)
	if code != 404 || !strings.Contains(body, "commit "+elsewhere+" is not on AppTown yet") || out["ok"] != nil || out["commit"] != nil || len(saw) != ran {
		t.Fatalf("a commit not on AppTown: %d %s, ran %d checks", code, body, len(saw))
	}
	if code, body, _ := check(`{"commit":"HEAD"}`); code != 400 || !strings.Contains(body, "not a full commit id") {
		t.Fatalf("a revision name as the commit: %d %s", code, body)
	}
	if code, body, out := check(`{}`); code != 200 || out["commit"] != head {
		t.Fatalf("no commit named: %d %s", code, body)
	}
}

// `apptown check --published` (the body {"published":true}, as the CLI sends
// it: cmd/apptown/check_test.go) checks the tip of the change's branch in the
// project's repository, not the change's folder, whose HEAD is newer work
// not yet published; it moves no branch. An empty body keeps its meaning,
// the change's latest commit on AppTown (here the folder's HEAD). A
// change whose branch the repository does not have is refused, with no
// check run.
func TestAppTownCheckPublishedChecksTheBranchTipNotTheFolder(t *testing.T) {
	t.Parallel()
	a, ch := step1ReviewGuidance(t, true, `[{"appliesTo": ["repository"], "name":"clean","evaluator":"sh","sh":"true"}]`)
	var saw []string // work.txt in each tree a check ran on
	installCheckEffects(t, a, checkOverrides{run: func(req execRequest) execOutcome {
		body, _ := os.ReadFile(filepath.Join(req.Workspace, "work.txt"))
		saw = append(saw, string(body))
		return execOutcome{Termination: execution.Exited}
	}})
	ctx := context.Background()
	if _, err := a.Access.Bootstrap(ctx, identity.NewAccount{Username: "boss", Password: "test-password"}); err != nil {
		t.Fatal(err)
	}
	if err := a.EnsureAuthorization(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Access.EnsureUser(ctx, "owner"); err != nil {
		t.Fatal(err)
	}
	for _, cap := range []access.Capability{access.CapProjectRead, access.CapChangeRunChecks} {
		if _, err := a.Access.Grant(ctx, identity.Spec{SubjectType: "user", Subject: "owner", Capability: string(cap), Scope: access.ScopeProject, ScopeID: fmt.Sprint(ch.ProjectID), By: "boss"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	p := a.projectByID(ctx, ch.ProjectID)
	hub := a.projectRepoPath(p.Slug)
	ws := (workflow.Layout{DataDir: a.cfg.dataDir}).ChangeWorkspace(ch.ID)
	commit := func(body string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(ws, "work.txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		gittest.Run(t, ws, "add", "work.txt")
		gittest.Run(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", body)
		return gittest.Run(t, ws, "rev-parse", "HEAD")
	}
	published := commit("published\n")
	if err := mirrorBranchForTest(hub, ws, ch.Branch); err != nil {
		t.Fatal(err)
	}
	head := commit("the folder's newer work\n")
	check := func(body string) (int, string, map[string]any) {
		t.Helper()
		resp := authzCall(a.Handler(), "owner", "POST", fmt.Sprintf("/api/v1/changes/%d/check", ch.ID), body)
		var out map[string]any
		_ = json.Unmarshal(resp.Body.Bytes(), &out)
		return resp.Code, resp.Body.String(), out
	}
	tip := func() string { t.Helper(); return gittest.Run(t, hub, "rev-parse", "refs/heads/"+ch.Branch) }

	if code, body, out := check(`{"published":true}`); code != 200 || out["commit"] != published || out["ok"] != true || len(saw) == 0 || saw[0] != "published\n" {
		t.Fatalf("--published: %d %s, ran on %q; want the branch tip %s, not the folder's %s", code, body, saw, published, head)
	}
	if tip() != published || gittest.Run(t, ws, "rev-parse", "HEAD") != head {
		t.Fatalf("a published check moved something: branch %s, folder %s", tip(), gittest.Run(t, ws, "rev-parse", "HEAD"))
	}
	saw = nil
	if code, body, out := check(`{}`); code != 200 || out["commit"] != head || len(saw) == 0 || saw[0] != "the folder's newer work\n" {
		t.Fatalf("an empty request: %d %s, ran on %q; want the change's latest commit, the folder's %s", code, body, saw, head)
	}
	if tip() != published {
		t.Fatalf("an empty request moved the branch to %s", tip())
	}
	if code, body, _ := check(`{"published":true,"commit":"` + head + `"}`); code != 400 {
		t.Fatalf("a commit and the published tip together: %d %s", code, body)
	}

	// Nothing published: the repository has no branch for the change.
	// Removed with plain git as fixture setup: no product operation
	// unpublishes a change's branch while its change is open.
	gittest.Run(t, hub, "update-ref", "-d", "refs/heads/"+ch.Branch)
	saw = nil
	code, body, out := check(`{"published":true}`)
	if code != 404 || !strings.Contains(body, "nothing published to check") || !strings.Contains(body, ch.Branch) || out["commit"] != nil || len(saw) != 0 {
		t.Fatalf("no published branch: %d %s, ran %d checks", code, body, len(saw))
	}
}
