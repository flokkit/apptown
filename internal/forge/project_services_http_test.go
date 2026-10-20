package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/poll"
)

func TestProjectServicesHTTPKeepsDeclarationEditsOnProposedChange(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	rebuildWork(t, a, func(d *workflow.Dependencies) { d.Git = workflow.GitWorkspace{DataDir: a.cfg.dataDir} })
	if _, err := a.Dependencies.Save(context.Background(), "capability: postgres\ntitle: PostgreSQL\nlocal:\n  image: postgres:16-alpine\n", "boss"); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	base := "/api/v1/projects/game"
	if w := authzCall(h, "owner", "POST", base+"/repo", `{}`); w.Code != 200 {
		t.Fatalf("init: %d %s", w.Code, w.Body.String())
	}
	read := func(contract string, needs int) string {
		t.Helper()
		w := authzCall(h, "owner", "GET", base+"/services", "")
		var out struct {
			Contract    string
			Declaration string
			Needs       []json.RawMessage
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.Contract != contract || out.Needs == nil || len(out.Needs) != needs {
			t.Fatalf("services read: %d %s %v", w.Code, w.Body.String(), err)
		}
		return out.Declaration
	}
	start := read("none", 0)
	for _, tc := range []struct {
		who, body string
		code      int
	}{
		{"outsider", `{"needs":[{"capability":"postgres"}],"expectedDeclaration":""}`, 403},
		{"owner", `{`, 400},
		{"owner", `{"needs":[{"capability":"unknown"}],"expectedDeclaration":""}`, 422},
		// A save must name the declaration it started from.
		{"owner", `{"needs":[{"capability":"postgres"}]}`, 400},
	} {
		w := authzCall(h, tc.who, "POST", base+"/services", tc.body)
		if w.Code != tc.code {
			t.Fatalf("services refusal: %d %s", w.Code, w.Body.String())
		}
	}
	changes, err := a.Work.Changes(asPerson("owner"), idstest.N(1), workflow.ChangeList{})
	if err != nil || len(changes) != 1 {
		t.Fatalf("refusal opened work: %+v %v", changes, err)
	}
	w := authzCall(h, "owner", "POST", base+"/services", `{"needs":[{"capability":"postgres","params":{"database":"game"}}],"title":"Add persistent scores","expectedDeclaration":"`+start+`"}`)
	var out struct{ ChangeID ids.ID }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.ChangeID.IsZero() {
		t.Fatalf("propose: %d %s %v", w.Code, w.Body.String(), err)
	}
	ch, err := a.Work.Change(asPerson("owner"), out.ChangeID)
	if err != nil || ch.ProjectID != idstest.N(1) || ch.Title != "Add persistent scores" {
		t.Fatalf("proposal identity: %+v %v", ch, err)
	}
	ws := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", out.ChangeID))
	c, err := docker.ReadWorkspaceContract(ws)
	if err != nil || len(c.Needs) != 1 || c.Needs[0] != "postgres" || c.NeedParams["postgres"]["database"] != "game" {
		t.Fatalf("proposed needs: %+v %v", c, err)
	}
	read("none", 0) // no side effect on accepted main before landing
	if _, err := os.Stat(filepath.Join(a.projectRepoPath("game"), ".apptown", "app.yaml")); !os.IsNotExist(err) {
		t.Fatalf("main edited by proposal: %v", err)
	}
	// Main's declaration moves (as a landing would move it): an edit that
	// started from the older one is refused, and opens no Change.
	repo := a.projectRepoPath("game")
	if err := os.MkdirAll(filepath.Join(repo, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".apptown", "app.yaml"), []byte("app:\n  web: {build: ., ports: [\"8080\"], public: true}\nneeds:\n  redis: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-f", ".apptown/app.yaml"}, {"-c", "user.name=t", "-c", "user.email=t@localhost", "commit", "-qm", "landed"}} {
		if out, err := gitexec.Run(t.Context(), repo, args...); err != nil {
			t.Fatalf("%v: %s", args, out)
		}
	}
	w = authzCall(h, "owner", "POST", base+"/services", `{"needs":[{"capability":"postgres"}],"expectedDeclaration":"`+start+`"}`)
	if w.Code != 409 || w.Header().Get("X-AppTown-Deny-Code") != "settings-moved" {
		t.Fatalf("stale edit: %d %s", w.Code, w.Body.String())
	}
	if changes, err := a.Work.Changes(asPerson("owner"), idstest.N(1), workflow.ChangeList{}); err != nil || len(changes) != 2 {
		t.Fatalf("a refused edit opened work: %d %v", len(changes), err)
	}
	if now := read("app.yaml", 1); now == start {
		t.Fatal("the declaration's version did not move with main")
	}
}

// The wizard's doors (services seed, starter files, toolchain) write a new
// project's main directly. Once the project has a Change they are refused and
// main is left alone: from then on it changes only through a landing.
func TestMainIsWrittenOutsideALandingOnlyBeforeTheFirstChange(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	h := a.Handler()
	if _, err := a.Dependencies.Save(context.Background(), "capability: postgres\ntitle: PostgreSQL\nlocal:\n  image: postgres:16-alpine\n", "boss"); err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/projects", `{"slug":"fresh"}`); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	base := "/api/v1/projects/fresh"
	if w := authzCall(h, "boss", "POST", base+"/repo", `{}`); w.Code != 200 {
		t.Fatalf("init: %d %s", w.Code, w.Body.String())
	}
	repo := a.projectRepoPath("fresh")
	head := func() string {
		t.Helper()
		out, err := gitexec.Run(t.Context(), repo, "rev-parse", "HEAD")
		if err != nil {
			t.Fatalf("head: %s", out)
		}
		return out
	}
	born := head()
	wantMainRecorded(t, a, "fresh", scm.ViaInit)
	if w := authzCall(h, "boss", "POST", base+"/services", `{"needs":[{"capability":"postgres"}],"direct":true}`); w.Code != 200 {
		t.Fatalf("seed services: %d %s", w.Code, w.Body.String())
	}
	wantMainRecorded(t, a, "fresh", scm.ViaGenesis)
	if w := authzCall(h, "boss", "POST", base+"/toolchain", `{"name":"go"}`); w.Code != 200 {
		t.Fatalf("seed toolchain: %d %s", w.Code, w.Body.String())
	}
	wantMainRecorded(t, a, "fresh", scm.ViaGenesis)
	// A new project's declaration is still its own: never overwritten.
	if w := authzCall(h, "boss", "POST", base+"/toolchain", `{"name":"python"}`); w.Code != 409 {
		t.Fatalf("replace toolchain: %d %s", w.Code, w.Body.String())
	}
	seeded := head()
	if seeded == born {
		t.Fatal("the new project's seeds were not committed")
	}
	if log, _ := gitexec.Run(t.Context(), repo, "log", "--format=%B"); !strings.Contains(log, "AppTown-Actor: boss") {
		t.Fatalf("seed commits do not name their actor:\n%s", log)
	}
	p, err := a.Projects.BySlug(context.Background(), "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.Create(asPerson("boss"), workflow.ChangeDraft{ProjectID: p.ID, Title: "first change"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"starter", "apptown"} {
		if _, err := a.Ways.PublishPack(t.Context(), name, "A pack", `{}`, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(repo, "mise.toml")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, body string }{
		{"/services", `{"needs":[{"capability":"postgres","params":{"version":"17"}}],"direct":true}`},
		{"/toolchain", `{"name":"go"}`},
	} {
		w := authzCall(h, "boss", "POST", base+tc.path, tc.body)
		if w.Code != 409 || w.Header().Get("X-AppTown-Deny-Code") != "project-established" {
			t.Fatalf("%s on an established project: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	// A pack can still be adopted; its files are named as not written.
	w := authzCall(h, "boss", "POST", base+"/pack", `{"name":"starter"}`)
	var applied struct{ Skipped []string }
	if err := json.Unmarshal(w.Body.Bytes(), &applied); err != nil || w.Code != 200 || len(applied.Skipped) != 1 {
		t.Fatalf("pack on an established project: %d %s %v", w.Code, w.Body.String(), err)
	}
	if head() != seeded {
		t.Fatal("an established project's main moved outside a landing")
	}
}

// A landing can reach main between a services edit's check and the clone of
// its Change's workspace. The edit is judged on that workspace, so it is
// refused rather than proposing bytes computed from the older main, and the
// Change opened for it is closed with the reason.
func TestProjectServicesEditIsJudgedOnTheWorkspaceItCommitsTo(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	rebuildWork(t, a, func(d *workflow.Dependencies) { d.Git = workflow.GitWorkspace{DataDir: a.cfg.dataDir} })
	if _, err := a.Dependencies.Save(context.Background(), "capability: postgres\ntitle: PostgreSQL\nlocal:\n  image: postgres:16-alpine\n", "boss"); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/repo", `{}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	repo := a.projectRepoPath("game")
	write := func(image string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(repo, ".apptown"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".apptown", "app.yaml"), []byte("app:\n  web: {image: "+image+", ports: [\"8080\"], public: true}\n"), 0644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", "-f", ".apptown/app.yaml"}, {"-c", "user.name=t", "-c", "user.email=t@localhost", "commit", "-qm", image}} {
			if out, err := gitexec.Run(t.Context(), repo, args...); err != nil {
				t.Fatalf("commit: %s %v", out, err)
			}
		}
	}
	write("nginx:1")
	w := authzCall(h, "owner", "GET", "/api/v1/projects/game/services", "")
	var before struct{ Declaration string }
	if err := json.Unmarshal(w.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	release, err := a.repositories.Acquire(context.Background(), idstest.N(1))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- authzCall(h, "owner", "POST", "/api/v1/projects/game/services", `{"needs":[{"capability":"postgres"}],"expectedDeclaration":"`+before.Declaration+`"}`)
	}()
	if !poll.Until(context.Background(), 5*time.Second, 10*time.Millisecond, func() bool {
		changes, err := a.Work.Changes(asPerson("owner"), idstest.N(1), workflow.ChangeList{})
		if err != nil {
			t.Fatal(err)
		}
		return len(changes) > 1
	}) {
		t.Fatal("save did not reach workspace preparation")
	}
	// A landing owns the repository lock. The save has already read/edited the old
	// declaration and committed its Change, but cannot yet clone the accepted tip.
	write("nginx:2")
	release()
	select {
	case w = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("save did not finish")
	}
	if w.Code != 409 || w.Header().Get("X-AppTown-Deny-Code") != "settings-moved" {
		t.Fatalf("an edit whose base moved: %d %s", w.Code, w.Body.String())
	}
	changes, err := a.Work.Changes(asPerson("owner"), idstest.N(1), workflow.ChangeList{})
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range changes {
		if ch.Title == "Update services" && ch.State != workflow.ChangeAbandoned {
			t.Fatalf("the unused change stays open: %+v", ch)
		}
	}
	c, err := docker.ReadWorkspaceContract(repo)
	if err != nil || c.PublicPart().Image != "nginx:2" {
		t.Fatalf("main: %+v %v", c, err)
	}
}

// wantMainRecorded: AppTown's record of a project's main is the hub's main,
// recorded by the door named, read from the owner.
func wantMainRecorded(t *testing.T, a *App, slug string, via scm.MainVia) {
	t.Helper()
	p, err := a.Projects.BySlug(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	hub := a.projectRepoPath(slug)
	m, known, err := scm.ReadMain(context.Background(), a.DB, p.ID)
	if err != nil || !known {
		t.Fatalf("%s: no recorded main (%v)", slug, err)
	}
	if want := scm.Ref(t.Context(), hub, "refs/heads/"+p.DefaultBranch); m.SHA != want || m.Via != string(via) {
		t.Fatalf("%s: recorded main %s by %s, want the hub's %s by %s", slug, m.SHA, m.Via, want, via)
	}
}
