package forge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/consoletest"
	"flokkit.io/forge/internal/forge/agentenv"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestConsoleLifecycleHTTPValidatesBeforeAdmissionAndEndsOnlyItsSession(t *testing.T) {
	t.Parallel()
	agent := consoletest.NewAgent()
	cli := fixtureCLI(t)
	const runtimeURL = "http://worker.test:7779"
	a := authzFixture(t, Config{DockerClient: newFakeDocker(), ConsoleCLI: cli, ConsoleAgent: agent,
		InferenceURL: runtimeURL, InferenceNetworks: []string{"127.0.0.1/32"}})
	configureConsoleModel(t, a)
	// First use must work without a previous console creating the directory.
	if _, err := os.Stat(filepath.Join(a.cfg.dataDir, "console")); !os.IsNotExist(err) {
		t.Fatalf("expected a fresh console directory, got %v", err)
	}
	h := a.Handler()
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"model":`, 400}, {`{"model":"fake/model","changeId":"wrong type"}`, 400},
		{`{"model":"fake/model","changeId":"0000000f-423f-7000-8000-000000000000"}`, 404},
		{`{"model":"fake/model","changeId":"` + fixtureWork(t, a).Change.String() + `"}`, 409}, // this fixture's change has no writable branch
	} {
		w := authzCall(h, "owner", "POST", "/api/v1/projects/game/console", tc.body)
		if w.Code != tc.status {
			t.Fatalf("rejected create: %d %s", w.Code, w.Body.String())
		}
		if got := a.Console.List(t.Context(), console.Match{}); len(got) != 0 {
			t.Fatalf("bad request opened sessions: %+v", got)
		}
	}
	if w := authzCall(h, "outsider", "POST", "/api/v1/projects/game/console", `{"model":"fake/model"}`); w.Code != 403 {
		t.Fatalf("foreign project create: %d %s", w.Code, w.Body.String())
	}
	w := authzCall(h, "owner", "POST", "/api/v1/projects/game/console", `{"model":"fake/model"}`)
	var out struct {
		Session struct {
			ID, ProjectSlug, Model string
			ChangeID               ids.ID
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 201 || out.Session.ID == "" || out.Session.ProjectSlug != "game" || out.Session.Model != "fake/model" || !out.Session.ChangeID.IsZero() {
		t.Fatalf("create: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	session, err := a.Console.Get(t.Context(), out.Session.ID)
	if err != nil || session.Actor != "owner" || conversation.Of(session).ProjectID != idstest.N(1) {
		t.Fatalf("admitted session: %+v %v", session, err)
	}
	if strings.Contains(w.Body.String(), session.Token) {
		t.Fatal("session response exposed its token")
	}
	launch := agent.Started(session.ID)
	if launch.Model != "fake/model" || !slices.Contains(launch.Env, "FORGE_SESSION_TOKEN="+session.Token) || !slices.Contains(launch.Mounts, cli+":"+agentenv.CLIDir+"/apptown:ro") {
		t.Fatal("console launch did not receive the selected model, session credential and CLI mount")
	}
	models, err := os.ReadFile(filepath.Join(session.Dir, "pi-home", ".pi", "agent", "models.json"))
	if err != nil || !strings.Contains(string(models), runtimeURL) || !strings.Contains(string(models), "$FORGE_SESSION_TOKEN") || strings.Contains(string(models), "provider-canary") {
		t.Fatalf("console model file must use the gateway and session credential: %v", err)
	}
	// Maintenance must observe the same runtime selected for console launches.
	session.SetLastUsed(time.Now().Add(-3 * time.Hour))
	if got := a.reaper.Containers(context.Background(), true); !slices.Contains(got, "forge-console-"+session.ID) || !agent.Running(session.ID) {
		t.Fatalf("maintenance dry run did not observe the selected runtime: %v", got)
	}
	session.Touch()
	// Empty bodies have always meant an ordinary project console. Keep that
	// intentional default distinct from malformed or partially decoded JSON.
	r := httptest.NewRequest("POST", "/api/v1/projects/open/console", nil)
	r.Header.Set("X-Forge-Actor", "outsider")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var other struct{ Session struct{ ID string } }
	if err := json.Unmarshal(w.Body.Bytes(), &other); err != nil || w.Code != 201 || other.Session.ID == "" {
		t.Fatalf("empty-body create: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	path := "/api/v1/console/" + session.ID
	if w := authzCall(h, "outsider", "DELETE", path, ""); w.Code != 403 || !agent.Running(session.ID) {
		t.Fatalf("foreign end: %d %s", w.Code, w.Body.String())
	}
	w = authzCall(h, "owner", "DELETE", path, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) || agent.Running(session.ID) || !agent.Running(other.Session.ID) {
		t.Fatalf("end selected session: %d %s", w.Code, w.Body.String())
	}
	if _, ok := a.Console.Identity(session.Token); ok {
		t.Fatal("ended session credential still active")
	}
	if w := authzCall(h, "owner", "DELETE", path, ""); w.Code != 404 {
		t.Fatalf("end again: %d %s", w.Code, w.Body.String())
	}
	// Daemon shutdown releases control while preserving long-lived sessions.
	a.StopConsoles(r.Context())
	if _, err := a.Console.Get(t.Context(), other.Session.ID); err != nil || !agent.Running(other.Session.ID) {
		t.Fatalf("shutdown must retain the borrowed runtime and session: %v", err)
	}
	if w := authzCall(h, "outsider", "DELETE", "/api/v1/console/"+other.Session.ID, ""); w.Code != 200 {
		t.Fatalf("other session end: %d %s", w.Code, w.Body.String())
	}
}

func TestChangeConsoleHTTPUsesWritableCheckoutAndRetainsItAfterEnd(t *testing.T) {
	t.Parallel()
	agent := consoletest.NewAgent()
	a := workAuthorityFixture(t, Config{DockerClient: newFakeDocker(), ConsoleCLI: fixtureCLI(t), ConsoleAgent: agent,
		InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
	fx := fixtureWork(t, a)
	configureConsoleModel(t, a)
	rebuildWork(t, a, func(deps *workflow.Dependencies) { deps.Git = workflow.GitWorkspace{DataDir: a.cfg.dataDir} })
	h := a.Handler()
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/repo", `{}`); w.Code != 200 {
		t.Fatalf("initialize repository: %d %s", w.Code, w.Body.String())
	}
	change, err := a.Work.Create(asPerson("owner"), workflow.ChangeDraft{ProjectID: idstest.N(1), Title: "Editable work"})
	if err != nil || change.Branch == "" {
		t.Fatalf("create: %+v %v", change, err)
	}
	body := fmt.Sprintf(`{"model":"fake/model","changeId":%q}`, change.ID)
	if w := authzCall(h, "boss", "POST", "/api/v1/projects/open/console", body); w.Code != 404 {
		t.Fatalf("cross-project attachment: %d %s", w.Code, w.Body.String())
	}
	if got := a.Console.List(t.Context(), console.Match{}); len(got) != 0 {
		t.Fatalf("rejected attachment opened chat: %+v", got)
	}
	w := authzCall(h, "owner", "POST", "/api/v1/projects/game/console", body)
	var out struct {
		Session struct {
			ID       string
			ChangeID ids.ID
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 201 || out.Session.ChangeID != change.ID {
		t.Fatalf("open: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	session, err := a.Console.Get(t.Context(), out.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", change.ID))
	if !session.Workspace().Writable || session.Workspace().Owned || session.Workspace().Path != want || !session.Docker {
		t.Fatalf("wrong checkout: %+v", session)
	}
	if err := os.WriteFile(filepath.Join(want, "chat-edit.txt"), []byte("keep my edit"), 0600); err != nil {
		t.Fatal(err)
	}

	if w := authzCall(h, "owner", "POST", "/api/v1/console/"+session.ID+"/say", `{"text":"Keep my work"}`); w.Code != 200 {
		t.Fatalf("send: %d %s", w.Code, w.Body.String())
	}
	agent.Reply(session.ID, "It is saved.")
	if w := authzCall(h, "owner", "DELETE", "/api/v1/console/"+session.ID, ""); w.Code != 200 {
		t.Fatalf("end: %d %s", w.Code, w.Body.String())
	}
	if b, err := os.ReadFile(filepath.Join(want, "chat-edit.txt")); err != nil || string(b) != "keep my edit" {
		t.Fatalf("ending borrowed checkout lost edits: %q %v", b, err)
	}
	transcriptPath := fmt.Sprintf("/api/v1/changes/%d/transcript", change.ID)
	if w := authzCall(h, "outsider", "GET", transcriptPath, ""); w.Code != 403 || strings.Contains(w.Body.String(), "Keep my work") {
		t.Fatalf("private transcript: %d %s", w.Code, w.Body.String())
	}
	w = authzCall(h, "owner", "GET", transcriptPath, "")
	var transcript struct{ Sessions []console.Conversation }
	if err := json.Unmarshal(w.Body.Bytes(), &transcript); err != nil || w.Code != 200 || len(transcript.Sessions) != 1 || transcript.Sessions[0].ID != session.ID || transcript.Sessions[0].Actor != "owner" || len(transcript.Sessions[0].Messages) != 2 || transcript.Sessions[0].Messages[0].Text != "Keep my work" || transcript.Sessions[0].Messages[1].Text != "It is saved." {
		t.Fatalf("retained transcript: %d %s %v", w.Code, w.Body.String(), err)
	}
	w = authzCall(h, "owner", "GET", "/api/v1/changes/"+fx.Change.String()+"/transcript", "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"sessions":[],"unreadable":0}` {
		t.Fatalf("other change transcript: %d %s", w.Code, w.Body.String())
	}

}

// A project's past chats are exactly its ended chats opened on the project:
// not a live one, not an empty one, not a change's. Each names its opener
// and says whether it was moved here from another forge; a reader without
// evidence read gets none of it.
func TestProjectPastChatsAreItsEndedProjectChats(t *testing.T) {
	t.Parallel()
	agent := consoletest.NewAgent()
	a := workAuthorityFixture(t, Config{DockerClient: newFakeDocker(), ConsoleCLI: fixtureCLI(t), ConsoleAgent: agent,
		InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
	configureConsoleModel(t, a)
	rebuildWork(t, a, func(deps *workflow.Dependencies) { deps.Git = workflow.GitWorkspace{DataDir: a.cfg.dataDir} })
	h := a.Handler()
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/repo", `{}`); w.Code != 200 {
		t.Fatalf("initialize repository: %d %s", w.Code, w.Body.String())
	}
	change, err := a.Work.Create(asPerson("owner"), workflow.ChangeDraft{ProjectID: idstest.N(1), Title: "Chat work"})
	if err != nil {
		t.Fatal(err)
	}
	open := func(body, text string) string {
		t.Helper()
		w := authzCall(h, "owner", "POST", "/api/v1/projects/game/console", body)
		var out struct{ Session struct{ ID string } }
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 201 {
			t.Fatalf("open: %d %s", w.Code, w.Body.String())
		}
		if text != "" {
			if w := authzCall(h, "owner", "POST", "/api/v1/console/"+out.Session.ID+"/say", fmt.Sprintf(`{"text":%q}`, text)); w.Code != 200 {
				t.Fatalf("say: %d %s", w.Code, w.Body.String())
			}
			agent.Reply(out.Session.ID, "ok")
		}
		return out.Session.ID
	}
	end := func(id string) {
		t.Helper()
		if w := authzCall(h, "owner", "DELETE", "/api/v1/console/"+id, ""); w.Code != 200 {
			t.Fatalf("end: %d %s", w.Code, w.Body.String())
		}
	}
	open(`{"model":"fake/model"}`, "still going")
	ended := open(`{"model":"fake/model"}`, "what does it do")
	end(ended)
	end(open(`{"model":"fake/model"}`, ""))
	end(open(fmt.Sprintf(`{"model":"fake/model","changeId":%q}`, change.ID), "change words"))
	// No producer of a moved chat exists yet (import is a later step), so its
	// archive is written as the import will place it: an ended record whose
	// labels carry the import mark.
	moved := console.Meta{ID: a.Console.NewSessionID(), Scope: conversation.Key(idstest.N(1), ids.Nil), Actor: "alice on forge.example.com",
		Labels: map[string]string{conversation.ProjectLabel: idstest.N(1).String(), conversation.ImportedLabel: "1"}, CreatedAt: time.Now().Add(-time.Hour)}
	movedDir := filepath.Join(a.cfg.dataDir, "console", moved.ID)
	if err := os.MkdirAll(filepath.Join(movedDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(moved)
	if err := os.WriteFile(filepath.Join(movedDir, console.EndedFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	record := `{"type":"message","timestamp":"2026-10-01T09:00:00.000Z","message":{"role":"user","content":"make the duck faster"}}` + "\n"
	if err := os.WriteFile(filepath.Join(movedDir, "sessions", "a.jsonl"), []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}

	if w := authzCall(h, "outsider", "GET", "/api/v1/projects/game/transcript", ""); w.Code != 403 {
		t.Fatalf("a reader without evidence read: %d %s", w.Code, w.Body.String())
	}
	w := authzCall(h, "owner", "GET", "/api/v1/projects/game/transcript", "")
	var got struct {
		Sessions []struct {
			ID, Actor       string
			Ended, Imported bool
			Messages        []console.Message
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
		t.Fatalf("past chats: %d %s", w.Code, w.Body.String())
	}
	if len(got.Sessions) != 2 {
		t.Fatalf("want the ended project chat and the moved one, got %s", w.Body.String())
	}
	byID := map[string]int{got.Sessions[0].ID: 0, got.Sessions[1].ID: 1}
	mine, ok := byID[ended]
	if !ok || got.Sessions[mine].Actor != "owner" || !got.Sessions[mine].Ended || got.Sessions[mine].Imported || got.Sessions[mine].Messages[0].Text != "what does it do" {
		t.Fatalf("the ended local chat: %s", w.Body.String())
	}
	other, ok := byID[moved.ID]
	if !ok || got.Sessions[other].Actor != "alice on forge.example.com" || !got.Sessions[other].Imported {
		t.Fatalf("the moved chat: %s", w.Body.String())
	}
}

// A picture pasted into a chat stays visible after the chat ends, for
// anyone who may read the chat's history, on its change or its project:
// named by the chat and the picture's own name, never by a path, and only
// from a chat of that change or project. A moved chat's pictures come from
// its folder. Nothing is listed, and a deleted project's cleanup removes
// them with its ended chats.
func TestAnEndedChatsPicturesStayVisibleToItsReaders(t *testing.T) {
	t.Parallel()
	agent := consoletest.NewAgent()
	a := workAuthorityFixture(t, Config{DockerClient: newFakeDocker(), ConsoleCLI: fixtureCLI(t), ConsoleAgent: agent,
		InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
	configureConsoleModel(t, a)
	rebuildWork(t, a, func(deps *workflow.Dependencies) { deps.Git = workflow.GitWorkspace{DataDir: a.cfg.dataDir} })
	h := a.Handler()
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/repo", `{}`); w.Code != 200 {
		t.Fatalf("initialize repository: %d %s", w.Code, w.Body.String())
	}
	change, err := a.Work.Create(asPerson("owner"), workflow.ChangeDraft{ProjectID: idstest.N(1), Title: "Picture work"})
	if err != nil {
		t.Fatal(err)
	}
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	pasted := func(body string) string {
		t.Helper()
		w := authzCall(h, "owner", "POST", "/api/v1/projects/game/console", body)
		var out struct{ Session struct{ ID string } }
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 201 {
			t.Fatalf("open: %d %s", w.Code, w.Body.String())
		}
		if w := authzCall(h, "owner", "POST", "/api/v1/console/"+out.Session.ID+"/upload", fmt.Sprintf(`{"data":"data:image/png;base64,%s"}`, png)); w.Code != 200 {
			t.Fatalf("paste: %d %s", w.Code, w.Body.String())
		}
		if w := authzCall(h, "owner", "DELETE", "/api/v1/console/"+out.Session.ID, ""); w.Code != 200 {
			t.Fatalf("end: %d %s", w.Code, w.Body.String())
		}
		return out.Session.ID
	}
	projectChat := pasted(`{"model":"fake/model"}`)
	changeChat := pasted(fmt.Sprintf(`{"model":"fake/model","changeId":%q}`, change.ID))
	// No producer of a moved chat exists yet (import is a later step), so its
	// folder is written as the import will place it.
	moved := console.Meta{ID: a.Console.NewSessionID(), Scope: conversation.Key(idstest.N(1), ids.Nil), Actor: "alice on forge.example.com",
		Labels: map[string]string{conversation.ProjectLabel: idstest.N(1).String(), conversation.ImportedLabel: "1"}, CreatedAt: time.Now()}
	movedDir := filepath.Join(a.cfg.dataDir, "console", moved.ID)
	if err := os.MkdirAll(filepath.Join(movedDir, "uploads"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(moved)
	if err := os.WriteFile(filepath.Join(movedDir, console.EndedFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	picture, _ := base64.StdEncoding.DecodeString(png)
	if err := os.WriteFile(filepath.Join(movedDir, "uploads", "picture-1.png"), picture, 0o644); err != nil {
		t.Fatal(err)
	}
	// A link a runtime left in a chat's uploads never leads out of them.
	if err := os.Symlink(filepath.Join(a.cfg.dataDir, "console", moved.ID, console.EndedFile), filepath.Join(movedDir, "uploads", "picture-2.png")); err != nil {
		t.Fatal(err)
	}

	projectPath := func(chat, name string) string {
		return "/api/v1/projects/game/transcript/" + chat + "/pictures/" + name
	}
	changePath := func(chat, name string) string {
		return fmt.Sprintf("/api/v1/changes/%d/transcript/%s/pictures/%s", change.ID, chat, name)
	}
	for _, path := range []string{projectPath(projectChat, "picture-1.png"), projectPath(moved.ID, "picture-1.png"), changePath(changeChat, "picture-1.png")} {
		w := authzCall(h, "owner", "GET", path, "")
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), picture) {
			t.Fatalf("%s: %d %q", path, w.Code, w.Header().Get("Content-Type"))
		}
		if w := authzCall(h, "outsider", "GET", path, ""); w.Code != 403 {
			t.Fatalf("a reader without evidence read saw %s: %d", path, w.Code)
		}
	}
	for _, path := range []string{
		projectPath(projectChat, "picture-9.png"),                   // no such picture
		projectPath(changeChat, "picture-1.png"),                    // a change's chat, asked of the project
		changePath(projectChat, "picture-1.png"),                    // the project's chat, asked of a change
		projectPath(moved.ID, "picture-2.png"),                      // a link out of the uploads
		projectPath(projectChat, "..%2F"+console.EndedFile),         // a path, not a name
		projectPath(projectChat, "picture-1.png%2F..%2F..%2Fx.png"), // a path dressed as a name
		projectPath("..%2F"+projectChat, "picture-1.png"),           // a path, not a chat
		projectPath(projectChat, ""),                                // no listing
	} {
		if w := authzCall(h, "owner", "GET", path, ""); w.Code != 404 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}

	if failed := projectCleanupFor(a).Clean(t.Context(), idstest.N(1), projectlifecycle.CleanupPlan{Slug: "game"}); len(failed) != 0 {
		t.Fatalf("cleanup: %v", failed)
	}
	for _, chat := range []string{projectChat, changeChat, moved.ID} {
		if _, err := os.Stat(filepath.Join(a.cfg.dataDir, "console", chat)); !os.IsNotExist(err) {
			t.Fatalf("a deleted project's ended chat %s stayed: %v", chat, err)
		}
	}
}
