package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/workflowexec"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// scratchWorld is a change whose run has two live agent step attempts, a
// project repository, and a clone standing in for an agent's repository on
// a worker, with one commit AppTown does not have.
type scratchWorld struct {
	a              *App
	ch             *workflow.Change
	run            *workflow.Run
	tasks          map[ids.ID]*workflow.Execution
	steps          map[ids.ID]*workflow.Step
	first, second  ids.ID
	hub, work      string
	remote, commit string
	plane          *httptest.Server
}

func newScratchWorld(t *testing.T) *scratchWorld {
	t.Helper()
	a, _ := internalApp(t)
	_, ctx, ch := changeScenarioIn(t, a, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "implement", Uses: "agent/pi"}, {Name: "other", Uses: "agent/pi"}}}}})
	p := a.projectByID(ctx, ch.ProjectID)
	if err := a.lifecycle.InitializeRepository(ctx, p, "main", ""); err != nil {
		t.Fatal(err)
	}
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "test"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	w := &scratchWorld{a: a, ch: ch, run: run, tasks: map[ids.ID]*workflow.Execution{}, steps: map[ids.ID]*workflow.Step{}}
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{"agent/pi"}
		deps.Report = func(error) {}
		deps.Runners = map[string]workflow.Runner{"agent/pi": func(execution *workflow.Execution) {
			step, err := execution.Claim()
			if err != nil {
				t.Error(err)
				return
			}
			w.tasks[step.ID], w.steps[step.ID] = execution, step
		}}
	})
	a.Work.Stop()
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 2 {
		t.Fatalf("steps: %+v %v", snapshot, err)
	}
	w.first, w.second = snapshot.Steps[0].ID, snapshot.Steps[1].ID
	for _, id := range []ids.ID{w.first, w.second} {
		if err := a.Work.ExecuteStep(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if w.steps[w.first] == nil || w.steps[w.second] == nil {
		t.Fatal("the runner did not claim both steps")
	}
	w.hub = a.projectRepoPath(p.Slug)
	w.work = filepath.Join(t.TempDir(), "work")
	gittest.Run(t, t.TempDir(), "clone", "--quiet", w.hub, w.work)
	if err := os.WriteFile(filepath.Join(w.work, "work.txt"), []byte("in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, w.work, "add", "work.txt")
	gittest.Run(t, w.work, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "work in progress")
	w.commit = gittest.Run(t, w.work, "rev-parse", "HEAD")
	// The agent reaches AppTown through the read-only listener, as a
	// step on a worker does ($APPTOWN_URL).
	w.plane = httptest.NewServer(GETOnly(a.Handler()))
	t.Cleanup(w.plane.Close)
	w.remote = w.plane.URL + "/git/" + p.Slug
	return w
}

func (w *scratchWorld) token(step ids.ID) string {
	return w.a.runTok.Step(step, w.steps[step].Attempt)
}

func (w *scratchWorld) scratchRef(step ids.ID) string {
	return scm.ScratchRef(w.ch.ID, step, w.steps[step].Attempt)
}

// push runs git push from the agent's repository with token as the header
// the CLI sends.
func (w *scratchWorld) push(token string, args ...string) (string, error) {
	header := "http." + w.remote + ".extraHeader=Authorization: Bearer " + token
	return gitexec.Run(context.Background(), w.work, append([]string{"-c", header, "push", "--force", w.remote}, args...)...)
}

// A step attempt's runtime token writes exactly one ref: its own scratch
// ref, through the read-only listener. Main, the change's branch, any other
// ref and another step's scratch ref are refused, as are deleting it and
// fetching with the token; a newer commit replaces what an earlier check
// sent. Once the attempt settles its token pushes nothing, and its scratch
// ref is removed while a live attempt's stays.
func TestAStepPushesOnlyItsOwnScratchRef(t *testing.T) {
	t.Parallel()
	w := newScratchWorld(t)
	ctx := context.Background()
	tok := w.token(w.first)
	own := w.scratchRef(w.first)

	if out, err := w.push(tok, w.commit+":"+own); err != nil {
		t.Fatalf("the step's push to its own scratch ref: %v %s", err, out)
	}
	if got := gitexec.HubRef(ctx, w.hub, own); got != w.commit {
		t.Fatalf("the scratch ref holds %q, want %s", got, w.commit)
	}
	mainBefore := gitexec.HubRef(ctx, w.hub, "refs/heads/main")
	for name, dst := range map[string]string{
		"main":                     "refs/heads/main",
		"the change's branch":      "refs/heads/" + w.ch.Branch,
		"a new branch":             "refs/heads/elsewhere",
		"another AppTown ref":      "refs/apptown/incoming/ticket-1",
		"another step's scratch":   w.scratchRef(w.second),
		"another change's scratch": scm.ScratchRef(idstest.N(999999), w.first, w.steps[w.first].Attempt),
	} {
		if out, err := w.push(tok, w.commit+":"+dst); err == nil {
			t.Fatalf("%s: a step's push to %s was accepted: %s", name, dst, out)
		}
		if got := gitexec.HubRef(ctx, w.hub, dst); got != "" && dst != "refs/heads/main" {
			t.Fatalf("%s: %s now holds %s", name, dst, got)
		}
	}
	if got := gitexec.HubRef(ctx, w.hub, "refs/heads/main"); got != mainBefore {
		t.Fatalf("main moved from %s to %s", mainBefore, got)
	}
	// Two refs in one push, one of them its own: refused whole.
	if out, err := w.push(tok, w.commit+":"+own, w.commit+":refs/heads/elsewhere"); err == nil {
		t.Fatalf("a push naming its scratch ref and another was accepted: %s", out)
	}
	if out, err := w.push(tok, ":"+own); err == nil || gitexec.HubRef(ctx, w.hub, own) != w.commit {
		t.Fatalf("deleting the scratch ref: %v %s", err, out)
	}
	header := "http." + w.remote + ".extraHeader=Authorization: Bearer " + tok
	if out, err := gitexec.Run(ctx, w.work, "-c", header, "fetch", w.remote, "main"); err == nil {
		t.Fatalf("a step's token fetched: %s", out)
	}
	// The other step's token cannot write this step's scratch either.
	if out, err := w.push(w.token(w.second), w.commit+":"+own); err == nil {
		t.Fatalf("another step's token pushed this step's scratch ref: %s", out)
	}
	// An amended commit replaces what the earlier check sent.
	gittest.Run(t, w.work, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "--amend", "-qm", "work in progress, amended")
	amended := gittest.Run(t, w.work, "rev-parse", "HEAD")
	if out, err := w.push(tok, amended+":"+own); err != nil || gitexec.HubRef(ctx, w.hub, own) != amended {
		t.Fatalf("replacing the scratch ref: %v %s", err, out)
	}
	if out, err := w.push(w.token(w.second), amended+":"+w.scratchRef(w.second)); err != nil {
		t.Fatalf("the other step's own push: %v %s", err, out)
	}

	// The first attempt settles: its token pushes nothing more, and its
	// scratch ref goes; the live attempt's stays.
	if err := w.tasks[w.first].Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
		t.Fatal(err)
	}
	if out, err := w.push(tok, w.commit+":"+own); err == nil {
		t.Fatalf("a settled attempt's token pushed: %s", out)
	}
	if n, err := workflowexec.SweepScratchRefs(ctx, w.a.DB, w.a.cfg.dataDir); err != nil || n != 1 {
		t.Fatalf("the sweep removed %d (%v), want the settled attempt's one", n, err)
	}
	refs, err := scm.ScratchRefs(ctx, w.hub, scm.ScratchPrefix)
	if err != nil || !reflect.DeepEqual(refs, map[string]string{w.scratchRef(w.second): amended}) {
		t.Fatalf("scratch refs after settlement: %v %v", refs, err)
	}
}

// `apptown check` of a step's scratch commit checks it, through the API the
// CLI uses with the step's token, and answers that the result is feedback
// only. It writes no evidence: the run's record reads as it did, and no
// branch moves. A commit the step has not sent is still refused.
func TestAScratchCommitIsCheckedAsFeedbackOnly(t *testing.T) {
	t.Parallel()
	w := newScratchWorld(t)
	ctx := context.Background()
	tok := w.token(w.first)
	if out, err := w.push(tok, w.commit+":"+w.scratchRef(w.first)); err != nil {
		t.Fatalf("pushing the scratch ref: %v %s", err, out)
	}
	check := func(commit string) (int, map[string]any, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/changes/%d/check", w.plane.URL, w.ch.ID), strings.NewReader(`{"commit":"`+commit+`"}`))
		req.Header.Set("X-AppTown-Session-Token", tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := w.plane.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out, fmt.Sprint(out)
	}
	before, err := w.a.Work.Snapshot(asPerson("alice"), w.run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	branchBefore := gitexec.HubRef(ctx, w.hub, "refs/heads/"+w.ch.Branch)
	code, out, body := check(w.commit)
	if code != 200 || out["commit"] != w.commit || out["feedbackOnly"] != true || out["scratch"] != true || !strings.Contains(fmt.Sprint(out["note"]), "landing checks the published commit again") {
		t.Fatalf("checking the scratch commit: %d %s", code, body)
	}
	after, err := w.a.Work.Snapshot(asPerson("alice"), w.run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Steps, after.Steps) || before.Run.Status != after.Run.Status {
		t.Fatalf("a check of scratch work changed the run's record:\nbefore %+v\nafter  %+v", before.Steps, after.Steps)
	}
	if got := gitexec.HubRef(ctx, w.hub, "refs/heads/"+w.ch.Branch); got != branchBefore {
		t.Fatalf("a check of scratch work moved the change's branch from %q to %q", branchBefore, got)
	}
	gittest.Run(t, w.work, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "--allow-empty", "-qm", "not sent")
	if code, _, body := check(gittest.Run(t, w.work, "rev-parse", "HEAD")); code != 404 || !strings.Contains(body, "not on AppTown yet") {
		t.Fatalf("a commit the step did not send: %d %s", code, body)
	}
}

// A step's token reads nothing from the project's repository, whatever the
// request's query says: Git's backend picks the operation from the method
// and path, so a receive-pack query on an upload-pack or file path is
// refused on the read-only listener and on AppTown's own, as are the
// upload-pack discovery and RPC, dumb discovery and file reads.
// Its one admitted request, the receive-pack discovery, advertises no
// sibling's branch.
func TestAStepTokenReadsNothingWhateverTheQuery(t *testing.T) {
	t.Parallel()
	w := newScratchWorld(t)
	// A sibling's unpublished commit, present only on its branch in the hub.
	gittest.Run(t, w.hub, "fetch", "--quiet", w.work, w.commit+":refs/heads/private-sibling")
	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	lsRefs := pkt("command=ls-refs\n") + "0001" + pkt("ref-prefix refs/heads/\n") + "0000"
	want := pkt("want "+w.commit+"\n") + "0000" + pkt("done\n")
	loose := "/objects/" + w.commit[:2] + "/" + w.commit[2:]
	direct := httptest.NewServer(w.a.Handler())
	t.Cleanup(direct.Close)
	// refused, when set, is the admission's own refusal, which AppTown's
	// listener gives: the step's token is turned away before git runs.
	type call struct{ name, method, path, protocol, body, leaked, refused string }
	calls := []call{
		{"ls-refs on upload-pack with a receive-pack query", "POST", "/git-upload-pack?service=git-receive-pack", "version=2", lsRefs, "private-sibling", ""},
		{"want on upload-pack with a receive-pack query", "POST", "/git-upload-pack?service=git-receive-pack", "", want, "PACK", ""},
		{"ls-refs on upload-pack", "POST", "/git-upload-pack", "version=2", lsRefs, "private-sibling", "a step's token only pushes"},
		{"want on upload-pack", "POST", "/git-upload-pack", "", want, "PACK", "a step's token only pushes"},
		{"upload-pack discovery", "GET", "/info/refs?service=git-upload-pack", "", "", "private-sibling", "a step's token only pushes"},
		{"dumb discovery", "GET", "/info/refs", "", "", "private-sibling", "a step's token only pushes"},
		{"dumb discovery with a receive-pack query", "GET", "/info/refs?service=git-receive-pack&service=git-upload-pack", "", "", "private-sibling", ""},
		{"GET of upload-pack with a receive-pack query", "GET", "/git-upload-pack?service=git-receive-pack", "", "", "", ""},
		{"a pack list with a receive-pack query", "GET", "/objects/info/packs?service=git-receive-pack", "", "", "", ""},
		{"a loose object with a receive-pack query", "GET", loose + "?service=git-receive-pack", "", "", "", ""},
		{"a loose object", "GET", loose, "", "", "", "a step's token only pushes"},
		{"HEAD with a receive-pack query", "GET", "/HEAD?service=git-receive-pack", "", "", "", ""},
		{"receive-pack with an upload-pack query", "POST", "/git-receive-pack?service=git-upload-pack", "version=2", lsRefs, "private-sibling", ""},
	}
	for listener, base := range map[string]string{"read-only": w.remote, "forge": direct.URL + strings.TrimPrefix(w.remote, w.plane.URL)} {
		for _, c := range calls {
			t.Run(listener+"/"+c.name, func(t *testing.T) {
				req, err := http.NewRequest(c.method, base+c.path, strings.NewReader(c.body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+w.token(w.first))
				if c.method == "POST" {
					req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
				}
				if c.protocol != "" {
					req.Header.Set("Git-Protocol", c.protocol)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				data, _ := io.ReadAll(resp.Body)
				if c.leaked != "" && strings.Contains(string(data), c.leaked) {
					t.Fatalf("a step's token read %q: status %d, %d bytes", c.leaked, resp.StatusCode, len(data))
				}
				if resp.StatusCode < 400 || resp.StatusCode >= 500 {
					t.Fatalf("want a refusal, got %d: %q", resp.StatusCode, data)
				}
				if c.refused != "" && listener == "forge" && (resp.StatusCode != http.StatusForbidden || !strings.Contains(string(data), c.refused)) {
					t.Fatalf("want the admission's refusal %q, got %d: %q", c.refused, resp.StatusCode, data)
				}
			})
		}
	}
	// The push's own discovery is admitted, and advertises no sibling.
	req, _ := http.NewRequest("GET", w.remote+"/info/refs?service=git-receive-pack", nil)
	req.Header.Set("Authorization", "Bearer "+w.token(w.first))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || strings.Contains(string(data), "private-sibling") || !strings.Contains(string(data), "refs/heads/main") {
		t.Fatalf("the receive-pack discovery: %d %q", resp.StatusCode, data)
	}
}

// A ticket's receive-pack discovery must not disclose another change's refs.
func TestATicketsPushDiscoveryShowsOnlyItsOwnRefs(t *testing.T) {
	t.Parallel()
	srv := httptest.NewUnstartedServer(nil)
	t.Cleanup(srv.Close)
	a, boss := mcpWriteFixture(t, Config{PublicHost: srv.Listener.Addr().String()})
	p, err := a.Projects.BySlug(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := a.Work.Create(a.asAppTown(context.Background(), p.ID, "test"), workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "ticket scope"})
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = a.Handler()
	srv.Start()
	res := mcp_call(t, a, boss, "checkout_change", map[string]any{"id": ch.ID})
	sc, ok := res["structuredContent"].(map[string]any)
	if !ok || res["isError"] == true {
		t.Fatalf("checkout: %v", res)
	}
	tok := sc["credential"].(map[string]any)["password"].(string)
	hub := a.projectRepoPath("app")
	privateWork := filepath.Join(t.TempDir(), "private")
	gittest.Run(t, t.TempDir(), "clone", "-q", hub, privateWork)
	if err := os.WriteFile(filepath.Join(privateWork, "secret.txt"), []byte("sibling private content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, privateWork, "add", "secret.txt")
	gittest.Run(t, privateWork, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "private sibling")
	private := gittest.Run(t, privateWork, "rev-parse", "HEAD")
	gittest.Run(t, hub, "fetch", "-q", privateWork, private+":refs/heads/private-sibling")
	req, _ := http.NewRequest("GET", srv.URL+"/git/app/info/refs?service=git-receive-pack", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("discovery refused: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "refs/heads/private-sibling") || strings.Contains(string(body), private) {
		t.Errorf("ticket receive-pack discovery leaks sibling name and tip: status=%d body=%q", resp.StatusCode, body)
	}
	remote := srv.URL + "/git/app"
	header := "http." + remote + ".extraHeader=Authorization: Bearer " + tok
	clone := filepath.Join(t.TempDir(), "ticket-clone")
	gittest.Run(t, t.TempDir(), "-c", header, "clone", "-q", remote, clone)
	if _, err := gitexec.Run(req.Context(), clone, "cat-file", "-e", private+":secret.txt"); err == nil {
		t.Fatal("fixture private file already cloned")
	}
	if out, err := gitexec.Run(req.Context(), clone, "-c", "protocol.version=2", "-c", header, "fetch", "-q", remote, private); err == nil {
		content := gittest.Run(t, clone, "show", "FETCH_HEAD:secret.txt")
		t.Errorf("ticket fetched the disclosed sibling commit through protocol v2: content=%q", content)
	} else {
		t.Logf("ticket hidden fetch refused: %s", out)
	}
}

// A pull credential must refuse a hidden commit even when its ID is known.
func TestAHiddenObjectIsNotFetchableByAnyProtocol(t *testing.T) {
	t.Parallel()
	w := newScratchWorld(t)
	gittest.Run(t, w.hub, "fetch", "-q", w.work, w.commit+":refs/heads/private-sibling")
	tok, err := w.a.Access.RuntimePullToken(context.Background(), w.ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	for _, version := range []string{"0", "2"} {
		t.Run(version, func(t *testing.T) {
			body := pkt("want "+w.commit+"\n") + "0000" + pkt("done\n")
			if version == "2" {
				body = pkt("command=fetch\n") + "0001" + pkt("want "+w.commit+"\n") + pkt("done\n") + "0000"
			}
			req, _ := http.NewRequest("POST", w.remote+"/git-upload-pack", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+tok)
			req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
			req.Header.Set("Git-Protocol", "version="+version)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "PACK") {
				t.Errorf("hidden commit fetched: status=%d bytes=%d", resp.StatusCode, len(data))
			}
			if !strings.Contains(string(data), "PACK") {
				t.Logf("version=%s status=%d response=%q", version, resp.StatusCode, data)
			}
			header := "http." + w.remote + ".extraHeader=Authorization: Bearer " + tok
			clone := filepath.Join(t.TempDir(), "pull-clone")
			gittest.Run(t, t.TempDir(), "-c", header, "clone", "-q", w.remote, clone)
			if _, err := gitexec.Run(req.Context(), clone, "cat-file", "-e", w.commit+":work.txt"); err == nil {
				t.Fatal("fixture private file already cloned")
			}
			if out, err := gitexec.Run(req.Context(), clone, "-c", "protocol.version="+version, "-c", header, "fetch", "-q", w.remote, w.commit); err == nil {
				t.Errorf("pull token fetched hidden file through protocol %s: content=%q", version, gittest.Run(t, clone, "show", "FETCH_HEAD:work.txt"))
			} else {
				t.Logf("git client refused protocol %s: %s", version, out)
			}
		})
	}
}
