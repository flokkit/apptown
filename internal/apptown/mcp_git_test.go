package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// Local Change contribution (mcp_git_tools.go, githttp.go): a ticket from
// checkout_change lets a real git client fetch main and push the one
// Change ref, from the expected tip only; sync_change makes the workspace
// adopt it; main and other refs stay closed.

func TestMCPCheckoutPushSync(t *testing.T) {
	t.Parallel()
	srv := httptest.NewUnstartedServer(nil)
	defer srv.Close()
	a, boss := mcpWriteFixture(t, Config{PublicHost: srv.Listener.Addr().String()})
	p, _ := a.Projects.BySlug(context.Background(), "app")
	hub := a.projectRepoPath("app")
	if err := scm.InstallNoPushHook(hub); err != nil {
		t.Fatal(err)
	}
	ch, err := a.Work.Create(a.asAppTown(context.Background(), p.ID, "test"), workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "local work", Body: "from a desktop"})
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = a.Handler()
	srv.Start()

	viewer := mcp_token(t, a, "viewer", "viewer", "member")
	before, err := a.Work.Change(asPerson("boss"), ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"checkout_change", "renew_change_grant", "sync_change"} {
		args := map[string]any{"id": ch.ID}
		if tool == "sync_change" {
			args["pushed_sha"] = strings.Repeat("a", 40)
		}
		res := mcp_call(t, a, viewer, tool, args)
		if res["isError"] != true || res["structuredContent"] != nil {
			t.Fatalf("viewer %s: %+v", tool, res)
		}
	}
	after, err := a.Work.Change(asPerson("boss"), ch.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("denied contribution changed work: %+v %v", after, err)
	}
	res := mcp_call(t, a, boss, "checkout_change", map[string]any{"id": ch.ID})
	sc, _ := res["structuredContent"].(map[string]any)
	if sc == nil || res["isError"] == true {
		t.Fatalf("checkout_change: %v", res)
	}
	cred := sc["credential"].(map[string]any)["password"].(string)
	repoURL := sc["repositoryUrl"].(string)
	expected := sc["expectedTip"].(string)
	if !strings.HasPrefix(cred, identity.TicketPrefix) || expected == "" || sc["ref"] != "refs/heads/"+ch.Branch {
		t.Fatalf("checkout facts: %v", sc)
	}
	authed := strings.Replace(repoURL, "http://", "http://x-apptown-ticket:"+cred+"@", 1)

	// Clone main, fetch the Change ref (visible to this ticket), commit, push.
	clone := filepath.Join(t.TempDir(), "c")
	gittest.Run(t, t.TempDir(), "clone", "--quiet", authed, clone)
	gittest.Run(t, clone, "remote", "set-url", "origin", authed)
	gittest.Run(t, clone, "fetch", "--quiet", "origin", "+refs/heads/"+ch.Branch+":refs/heads/"+ch.Branch)
	gittest.Run(t, clone, "switch", "--quiet", ch.Branch)
	_ = os.WriteFile(filepath.Join(clone, "NEW.md"), []byte("pushed from a desktop\n"), 0o644)
	gittest.Run(t, clone, "add", "-A")
	gittest.Run(t, clone, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "desktop work")
	pushed := gittest.Run(t, clone, "rev-parse", "HEAD")
	// main is closed even with the ticket.
	if out, err := gitexec.Run(t.Context(), clone, "push", "--quiet", "origin", ch.Branch+":main"); err == nil || !strings.Contains(out, "may push only") {
		t.Fatalf("pushing main must be refused by the hook: %v %s", err, out)
	}
	// Push the way the tool tells clients to: the ticket in APPTOWN_GIT_TICKET,
	// read by the inline credential helper, the remote URL clean.
	gittest.Run(t, clone, "remote", "set-url", "origin", repoURL)
	// An inherited helper may have a consumed ticket cached for this host.
	// Explicit ticket commands must take precedence over those credentials.
	gittest.Run(t, clone, "config", "credential.helper", "")
	gittest.Run(t, clone, "config", "--add", "credential.helper", `!f() { echo username=stale; echo password=stale; }; f`)
	var pushCmd []string
	for _, x := range sc["git"].(map[string]any)["push"].([]any) {
		pushCmd = append(pushCmd, x.(string))
	}
	if len(pushCmd) == 0 || pushCmd[0] != "git" || !strings.Contains(strings.Join(pushCmd, " "), "APPTOWN_GIT_TICKET") {
		t.Fatalf("the push command should read the ticket from the environment: %v", pushCmd)
	}
	// The one-line form carries the assignment itself, for fresh-shell tools.
	shell := sc["git"].(map[string]any)["shell"].(map[string]any)["push"].(string)
	if !strings.HasPrefix(shell, "APPTOWN_GIT_TICKET='"+cred+"' 'git'") {
		t.Fatalf("shell form should assign the ticket inline: %s", shell)
	}
	// The read-only listener takes no ticket push (there, a push is only a
	// step's scratch push): the same command, sent there, is refused and
	// spends nothing.
	ro := httptest.NewServer(GETOnly(a.Handler()))
	defer ro.Close()
	u, err := url.Parse(repoURL)
	if err != nil {
		t.Fatal(err)
	}
	roCmd := slices.Clone(pushCmd)
	roCmd[slices.Index(roCmd, "origin")] = ro.URL + u.Path
	rc := gittest.Command(roCmd[1:]...)
	rc.Dir = clone
	rc.Env = append(rc.Env, "APPTOWN_GIT_TICKET="+cred, "GIT_TERMINAL_PROMPT=0")
	if out, err := rc.CombinedOutput(); err == nil || !strings.Contains(string(out), "takes no Change ticket pushes") || scm.Ref(t.Context(), hub, "refs/heads/"+ch.Branch) == pushed {
		t.Fatalf("a ticket push through the read-only listener: %v %s", err, out)
	}
	pc := gittest.Command(pushCmd[1:]...)
	pc.Dir = clone
	pc.Env = append(pc.Env, "APPTOWN_GIT_TICKET="+cred, "GIT_TERMINAL_PROMPT=0")
	if out, err := pc.CombinedOutput(); err != nil {
		t.Fatalf("env-helper push: %v\n%s", err, out)
	}
	if got := scm.Ref(t.Context(), hub, "refs/heads/"+ch.Branch); got != pushed {
		t.Fatalf("hub ref after push: %s want %s", got, pushed)
	}
	// The ticket is consumed by the success: a second push needs a new one.
	_ = os.WriteFile(filepath.Join(clone, "NEW2.md"), []byte("again\n"), 0o644)
	gittest.Run(t, clone, "add", "-A")
	gittest.Run(t, clone, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "second")
	if out, err := gitexec.Run(t.Context(), clone, "push", "--quiet", "origin", ch.Branch+":"+ch.Branch); err == nil {
		t.Fatalf("a consumed ticket must not push again: %s", out)
	}
	if n := auditCount(t, a.DB, "change.revision-pushed"); n != 1 {
		t.Errorf("one push, one ledger row; got %d", n)
	}

	// sync_change: the workspace adopts the pushed tip.
	res = mcp_call(t, a, boss, "sync_change", map[string]any{"id": ch.ID, "pushed_sha": pushed, "expected_previous_tip": expected})
	sc, _ = res["structuredContent"].(map[string]any)
	if sc == nil || res["isError"] == true || sc["tip"] != pushed {
		t.Fatalf("sync_change: %v", res)
	}
	ws := filepath.Join(a.cfg.dataDir, "workspaces", "change-"+ch.ID.String())
	if got := gittest.Run(t, ws, "rev-parse", "HEAD"); got != pushed {
		t.Fatalf("workspace HEAD %s, want %s", got, pushed)
	}
	if _, err := os.Stat(filepath.Join(ws, "NEW.md")); err != nil {
		t.Fatal("the workspace did not adopt the pushed file")
	}

	// A fresh ticket expects the NEW tip; a push from a stale base is refused
	// by compare-and-swap even though the ticket is valid.
	res = mcp_call(t, a, boss, "renew_change_grant", map[string]any{"id": ch.ID})
	sc, _ = res["structuredContent"].(map[string]any)
	cred2 := sc["credential"].(map[string]any)["password"].(string)
	if sc["expectedTip"] != pushed {
		t.Fatalf("renewed ticket should expect the current tip: %v", sc["expectedTip"])
	}
	// Move the hub ahead behind the client's back (as another writer would).
	gittest.Run(t, ws, "commit", "-q", "--allow-empty", "-m", "someone else")
	if err := mirrorBranchForTest(hub, ws, ch.Branch); err != nil {
		t.Fatal(err)
	}
	authed2 := strings.Replace(repoURL, "http://", "http://x-apptown-ticket:"+cred2+"@", 1)
	gittest.Run(t, clone, "remote", "set-url", "origin", authed2)
	// The push lands in the ticket's holding ref; the hook refuses it with
	// facts because the Change moved since the ticket was minted.
	pushRef2 := scm.IncomingRef(idstest.Parse(t, sc["ticketId"].(string)))
	for _, force := range []bool{false, true} {
		args := []string{"push", "--quiet", "origin", ch.Branch + ":" + pushRef2}
		if force {
			args = append(args[:2], append([]string{"--force"}, args[2:]...)...)
		}
		if out, err := gitexec.Run(t.Context(), clone, args...); err == nil || !strings.Contains(out, "moved since your ticket") {
			t.Fatalf("a stale push (force %v) must be refused with facts: %v %s", force, err, out)
		}
	}
}

// A ticket superseded by a later one no longer holds the branch. Its push
// builds on the right tip, so only guarded publication can refuse it.
func TestMCPAnEarlierTicketCannotMoveTheBranch(t *testing.T) {
	t.Parallel()
	srv := httptest.NewUnstartedServer(nil)
	defer srv.Close()
	a, boss := mcpWriteFixture(t, Config{PublicHost: srv.Listener.Addr().String()})
	p, err := a.Projects.BySlug(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	if err := scm.InstallNoPushHook(a.projectRepoPath(p.Slug)); err != nil {
		t.Fatal(err)
	}
	ch, err := a.Work.Create(a.asAppTown(context.Background(), p.ID, "test"), workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "two tickets", Body: ""})
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = a.Handler()
	srv.Start()
	first, _ := mcp_call(t, a, boss, "checkout_change", map[string]any{"id": ch.ID})["structuredContent"].(map[string]any)
	second, _ := mcp_call(t, a, boss, "renew_change_grant", map[string]any{"id": ch.ID})["structuredContent"].(map[string]any)
	if first == nil || second == nil || first["expectedTip"] != second["expectedTip"] {
		t.Fatalf("two tickets on one tip: %v %v", first, second)
	}
	// Renewing replaced the earlier ticket: it is revoked, so it can neither
	// fetch nor push, and the branch does not move.
	hub := a.projectRepoPath(p.Slug)
	before := scm.Ref(t.Context(), hub, "refs/heads/"+ch.Branch)
	clone := filepath.Join(t.TempDir(), "clone")
	cred := first["credential"].(map[string]any)["password"].(string)
	authed := strings.Replace(first["repositoryUrl"].(string), "http://", "http://x-apptown-ticket:"+cred+"@", 1)
	if out, err := gitexec.Run(t.Context(), "", "clone", "-q", "--branch", p.DefaultBranch, authed, clone); err == nil {
		t.Fatalf("a superseded ticket could still fetch: %s", out)
	}
	if got := scm.Ref(t.Context(), hub, "refs/heads/"+ch.Branch); got != before {
		t.Fatalf("a superseded ticket moved the branch to %s", got)
	}
}

func TestMCPCheckoutRefusedWhileRunOwnsWorkspace(t *testing.T) {
	t.Parallel()
	a, boss := mcpWriteFixture(t)
	p, _ := a.Projects.BySlug(context.Background(), "app")
	ch, err := a.Work.Create(a.asAppTown(context.Background(), p.ID, "test"), workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "busy", Body: ""})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.recordAdoption(&p, "edit", "busy workspace fixture", "fixture"); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "implement", Version: 1}, Spec: schema.Spec{Kind: "lifecycle", Role: "working", Stages: []schema.Stage{{Name: "build", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}}}}})
	a.Work.Stop()
	if _, err := a.Work.StartRun(a.asAppTown(context.Background(), p.ID, "test"), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "implement"}); !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	res := mcp_call(t, a, boss, "checkout_change", map[string]any{"id": ch.ID})
	if res["isError"] != true || !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "is working on this change") {
		t.Fatalf("a running run must refuse a ticket with facts: %v", res)
	}
}

func TestMCPRefusesTicketAndDiffWhenWorkspaceCannotPublish(t *testing.T) {
	t.Parallel()
	a, boss := mcpWriteFixture(t)
	p, err := a.Projects.BySlug(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := a.Work.Create(a.asAppTown(context.Background(), p.ID, "test"), workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "unpublished work", Body: ""})
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	// The hub still has its old ref, but it is no longer the workspace's tip.
	gittest.Run(t, ws, "branch", "-m", ch.Branch, "moved-work")
	// The folder is not on the change's branch, so its work cannot be
	// shared: the checkout is refused, saying why, and no ticket is minted.
	res := mcp_call(t, a, boss, "checkout_change", map[string]any{"id": ch.ID})
	if res["isError"] != true || res["structuredContent"] != nil || !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "not the change's branch") {
		t.Fatalf("stale hub granted a ticket: %v", res)
	}
	requireAudits(t, a.DB, 0, "change.ticket-minted")
	if diff, err := contributionsFor(a).Diff(a.asAppTown(context.Background(), p.ID, "test"), ch.ID); err == nil || diff != "" {
		t.Fatalf("stale hub returned a diff: %q %v", diff, err)
	}
}

func TestMCPWorkspaceAvailabilityFailureCannotMintOrSync(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"checkout_change", "sync_change"} {
		t.Run(tool, func(t *testing.T) {
			t.Parallel()
			a, boss := mcpWriteFixture(t)
			p, err := a.Projects.BySlug(context.Background(), "app")
			if err != nil {
				t.Fatal(err)
			}
			ch, err := a.Work.Create(a.asAppTown(context.Background(), p.ID, "test"), workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "availability", Body: ""})
			if err != nil {
				t.Fatal(err)
			}
			hub := a.projectRepoPath(p.Slug)
			ws := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
			before := gittest.Run(t, ws, "rev-parse", "HEAD")
			args := map[string]any{"id": ch.ID}
			if tool == "sync_change" {
				if err := mirrorBranchForTest(hub, ws, ch.Branch); err != nil {
					t.Fatal(err)
				}
				gittest.Run(t, hub, "switch", ch.Branch)
				gittest.Run(t, hub, "commit", "--allow-empty", "-qm", "pushed work")
				args["pushed_sha"] = gittest.Run(t, hub, "rev-parse", "HEAD")
				args["expected_previous_tip"] = before
				gittest.Run(t, hub, "switch", p.DefaultBranch)
			}
			// Who holds the change cannot be read: nothing is granted.
			dbtest.FailNextStatement(t, a.DB, "name: Authority")
			w := mcp_post(a, boss, "", mcp_rpc(7, "tools/call", map[string]any{"name": tool, "arguments": args}))
			var response struct {
				Result json.RawMessage `json:"result"`
				Error  *rpcError       `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || response.Error == nil || len(response.Result) != 0 {
				t.Fatalf("unknown availability granted %s: %d %s", tool, w.Code, w.Body.String())
			}
			// sql-guarantee: identity has no read of tickets by kind; an unknown availability must mint no Git ticket row at all.
			completionCount(t, a.DB, 0, `SELECT count(*) FROM object_capabilities WHERE kind=$1`, identity.TicketKindGit)
			requireAudits(t, a.DB, 0, "change.ticket-minted", "change.revision-synced")
			if got := gittest.Run(t, ws, "rev-parse", "HEAD"); got != before {
				t.Fatalf("unknown availability changed workspace: %s -> %s", before, got)
			}
		})
	}
}

// A run that starts after checkout's first check must keep its branch.
func TestACheckoutRacingARunStartIsRefused(t *testing.T) {
	t.Parallel()
	a, _ := mcpWriteFixture(t)
	p, err := a.Projects.BySlug(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(asPerson("boss"), 15*time.Second)
	defer cancel()
	ch, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "checkout race"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.recordAdoption(&p, "edit", "checkout race fixture", "fixture"); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "implement", Version: 1}, Spec: schema.Spec{Kind: "lifecycle", Role: "working", Stages: []schema.Stage{{Name: "build", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}}}}})
	a.Work.Stop()
	// sql-guarantee: pause ticket issuance after the early run check and
	// workspace publication; an owner API cannot suspend this SQL statement.
	completionExec(t, a.DB, `CREATE FUNCTION hold_checkout_ticket() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(8675354); RETURN NEW; END $$`)
	completionExec(t, a.DB, `CREATE TRIGGER hold_checkout_ticket BEFORE INSERT ON object_capabilities FOR EACH ROW EXECUTE FUNCTION hold_checkout_ticket()`)
	gate := holdAdvisoryLock(t, ctx, a.DB, 8675354)
	defer gate.Rollback()
	done := make(chan error, 1)
	go func() { _, err := contributionsFor(a).Checkout(ctx, ch.ID); done <- err }()
	awaitAdvisoryWaiter(t, ctx, a.DB, 8675354, done)
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "implement"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	before, err := scm.ReadAuthorityIn(ctx, a.DB, p.ID, ch.Branch)
	if err != nil || before.Holder != scm.RunHolder(run.ID) {
		t.Fatalf("run did not take the branch: %+v %v", before, err)
	}
	if err := gate.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "is working on this change") {
			t.Fatalf("checkout must refuse the newly active run: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("checkout never finished")
	}
	after, err := scm.ReadAuthorityIn(ctx, a.DB, p.ID, ch.Branch)
	if err != nil || after != before {
		t.Fatalf("checkout displaced the run: before=%+v after=%+v err=%v", before, after, err)
	}
	// sql-guarantee: identity has no read of live tickets by change; the refusal must leave none.
	completionCount(t, a.DB, 0, `SELECT COUNT(*) FROM object_capabilities WHERE revoked_at IS NULL`)
}

// A desktop checkout holds its change until its push is synced: a run is
// refused meanwhile and never takes the change from it, and the ticket can
// still publish its push.
func TestARunIsRefusedWhileACheckoutHoldsTheChange(t *testing.T) {
	t.Parallel()
	a, _ := mcpWriteFixture(t)
	p, err := a.Projects.BySlug(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	ctx := asPerson("boss")
	ch, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "run after checkout"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.recordAdoption(&p, "edit", "checkout race fixture", "fixture"); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "implement", Version: 1}, Spec: schema.Spec{Kind: "lifecycle", Role: "working", Stages: []schema.Stage{{Name: "build", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}}}}})
	a.Work.Stop()
	checkout, err := contributionsFor(a).Checkout(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	var held *workflow.ChangeHeld
	if _, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "implement"}); !errors.As(err, &held) {
		t.Fatalf("a run beside a checkout: %v", err)
	}
	authority, err := scm.ReadAuthorityIn(ctx, a.DB, p.ID, ch.Branch)
	if err != nil || authority.Holder != scm.TicketHolder(checkout.Ticket.ID) {
		t.Fatalf("the checkout lost the change: %+v %v", authority, err)
	}
	hub := a.projectRepoPath("app")
	gittest.Run(t, hub, "update-ref", checkout.PushRef, checkout.Tip)
	if err := contributionsFor(a).PublishPush(ctx, checkout.Ticket); err != nil {
		t.Fatalf("the holding ticket could not publish: %v", err)
	}
}

// The boot backfill names every hub it left without the no-push hook and
// still installs it on the others; the start then refuses to serve.
func TestTheNoPushBackfillNamesEachHubItCouldNotProtect(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	root := filepath.Join(a.cfg.dataDir, "workspaces")
	good, bad := filepath.Join(root, "project-good"), filepath.Join(root, "project-bad")
	for _, hub := range []string{good, bad} {
		if err := scm.InitializeRepository(t.Context(), hub, "main"); err != nil {
			t.Fatal(err)
		}
	}
	// Fixture: bad's hooks folder replaced by a file; good's hook removed.
	hooks := filepath.Join(bad, ".git", "hooks")
	if err := os.RemoveAll(hooks); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooks, []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(good, ".git", "hooks", "pre-receive")); err != nil {
		t.Fatal(err)
	}
	err := a.InstallNoPushHooks()
	if err == nil || !strings.Contains(err.Error(), bad) || strings.Contains(err.Error(), good) {
		t.Fatalf("backfill error %v; want it to name %s alone", err, bad)
	}
	if _, err := os.Stat(filepath.Join(good, ".git", "hooks", "pre-receive")); err != nil {
		t.Fatalf("the healthy hub did not get its hook: %v", err)
	}
	// A start's required repair refuses to go on serving the hub that
	// would accept pushes past landing.
	if err := a.RecoverInterruptedWork(t.Context()); err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("a start went on with a hub that accepts pushes: %v", err)
	}
}

// A landed push spends its ticket and records it though the client that
// pushed has hung up: a ticket left unspent would push again.
func TestALandedPushSpendsItsTicketThoughTheClientHangsUp(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAccounts(t, a.DB, "alice")
	_, ticket, err := a.Access.IssueTicket(context.Background(), "alice", ids.Nil, idstest.N(7), "refs/heads/apptown/change-00000000-0007-7000-8000-000000000000", "")
	if err != nil {
		t.Fatal(err)
	}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	spendPushedTicket(gone, a.Access, a.DB, audit.At(time.Now()), idstest.N(1), ticket, "a", "b")
	if again, err := a.Access.ConsumeTicket(context.Background(), ticket.ID); err != nil || again {
		t.Fatalf("the ticket was not spent: %v %v", again, err)
	}
	if n := auditCount(t, a.DB, "change.revision-pushed"); n != 1 {
		t.Fatalf("the push is not on the ledger: %d", n)
	}
}

// A checkout whose caller goes away after its ticket is issued, but before
// it takes the change, revokes the ticket anyway: an issued ticket that
// holds nothing must not stay live.
func TestACheckoutAbandonedAfterItsTicketRevokesIt(t *testing.T) {
	t.Parallel()
	a, _ := mcpWriteFixture(t)
	p, err := a.Projects.BySlug(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(asPerson("boss"), 15*time.Second)
	defer cancel()
	ch, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "abandoned checkout"})
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: pause the change's taking after its ticket is issued; an
	// owner API cannot suspend this SQL statement.
	completionExec(t, a.DB, `CREATE FUNCTION hold_checkout_take() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.holder LIKE 'ticket:%' THEN PERFORM pg_advisory_xact_lock(8675355); END IF; RETURN NEW; END $$`)
	completionExec(t, a.DB, `CREATE TRIGGER hold_checkout_take BEFORE INSERT OR UPDATE ON branch_authority FOR EACH ROW EXECUTE FUNCTION hold_checkout_take()`)
	gate := holdAdvisoryLock(t, ctx, a.DB, 8675355)
	defer gate.Rollback()
	caller, hangUp := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := contributionsFor(a).Checkout(caller, ch.ID); done <- err }()
	awaitAdvisoryWaiter(t, ctx, a.DB, 8675355, done)
	hangUp()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a checkout whose caller hung up took the change")
		}
	case <-ctx.Done():
		t.Fatal("checkout never finished")
	}
	if err := gate.Rollback(); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: identity has no read of tickets by change; the checkout issued one, revoked, and none is left live.
	completionCount(t, a.DB, 1, `SELECT COUNT(*) FILTER (WHERE revoked_at IS NOT NULL) * 10 + COUNT(*) FILTER (WHERE revoked_at IS NULL) - 9 FROM object_capabilities`)
}

// holdAdvisoryLock holds advisory lock id in a transaction the caller rolls
// back: the barrier an injected trigger waits on.
func holdAdvisoryLock(t *testing.T, ctx context.Context, db *database.DB, id int64) *database.Tx {
	t.Helper()
	gate, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: takes the advisory lock an injected trigger waits on; a held lock, not a read
	if _, err := gate.SQL().ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, id); err != nil {
		_ = gate.Rollback()
		t.Fatal(err)
	}
	return gate
}

// awaitAdvisoryWaiter returns once some statement waits on advisory lock
// id; the operation under test finishing first, or ctx ending, fails.
func awaitAdvisoryWaiter(t *testing.T, ctx context.Context, db *database.DB, id int64, done <-chan error) {
	t.Helper()
	for {
		var waiting bool
		// sql-guarantee: only Postgres's lock table can say an operation is waiting at the injected barrier; no owner read can
		if err := db.SQL().QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=$1 AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`, id).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("the operation did not reach the barrier: %v", err)
		case <-ctx.Done():
			t.Fatal("the operation did not reach the barrier")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// A desktop checkout's ticket is a credential: when its receipt cannot be
// written, the checkout fails, the ticket is revoked and the change is not
// held by it.
func TestACheckoutWhoseTicketReceiptFailsHandsOutNoTicket(t *testing.T) {
	t.Parallel()
	a, _ := mcpWriteFixture(t)
	p, err := a.Projects.BySlug(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	ctx := asPerson("boss")
	ch, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "unrecorded ticket"})
	if err != nil {
		t.Fatal(err)
	}
	restore := a.DB.FailNextStatement("-- name: AppendReceipt")
	out, err := contributionsFor(a).Checkout(ctx, ch.ID)
	restore()
	if err == nil || out.Token != "" {
		t.Fatalf("a checkout whose receipt failed handed out a ticket: %+v %v", out, err)
	}
	held, err := scm.ReadAuthorityIn(ctx, a.DB, p.ID, ch.Branch)
	if err != nil || strings.HasPrefix(held.Holder, "ticket") {
		t.Fatalf("the unrecorded ticket holds the change: %+v %v", held, err)
	}
}
