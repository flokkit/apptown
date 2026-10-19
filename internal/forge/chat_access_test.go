package forge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/consoletest"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/poll"
)

// chatAccessWorld is the authz fixture's project "game" with a project chat
// the owner opened through the route, a viewer (vic) and a contributor
// (jenny) beside them.
type chatAccessWorld struct {
	a     *App
	agent *consoletest.Agent
	chat  string
}

func newChatAccessWorld(t *testing.T) chatAccessWorld {
	t.Helper()
	agent := consoletest.NewAgent()
	a := authzFixture(t, Config{DockerClient: newFakeDocker(), ConsoleCLI: fixtureCLI(t), ConsoleAgent: agent,
		InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
	configureConsoleModel(t, a)
	viewerAndContributor(t, a)
	w := authzCall(a.Handler(), "owner", "POST", "/api/v1/projects/game/console", `{"model":"fake/model"}`)
	var out struct{ Session struct{ ID string } }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 201 {
		t.Fatalf("the owner's chat: %d %s", w.Code, w.Body.String())
	}
	t.Cleanup(func() { _, _ = a.Console.End(context.Background(), out.Session.ID) })
	return chatAccessWorld{a: a, agent: agent, chat: out.Session.ID}
}

// listed is the chat as the project's chat list shows it to who.
func (w chatAccessWorld) listed(t *testing.T, who string) (openedBy string, mayAct bool) {
	t.Helper()
	resp := authzCall(w.a.Handler(), who, "GET", "/api/v1/projects/game/console", "")
	var out struct {
		Sessions []struct {
			ID, OpenedBy string
			MayAct       bool
		}
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil || resp.Code != 200 {
		t.Fatalf("%s lists the chats: %d %s", who, resp.Code, resp.Body.String())
	}
	for _, s := range out.Sessions {
		if s.ID == w.chat {
			return s.OpenedBy, s.MayAct
		}
	}
	t.Fatalf("%s does not see the chat: %s", who, resp.Body.String())
	return "", false
}

// closeTransparentReads revokes the default that everyone signed in reads
// every project, as an institution may: then only a project's readers
// read it.
func closeTransparentReads(t *testing.T, a *App) {
	t.Helper()
	grants, err := a.Access.GrantsAt(context.Background(), access.ScopeInstance, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range grants {
		if g.Subject == identity.GroupAllUsers && g.RoleName == access.RoleAuditor {
			if _, err := a.Access.Revoke(context.Background(), g.ID, "boss"); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// closeTerminals closes a test's terminal sockets and waits until the chat
// has let go of them: a typist's terminal writes the chat's record as it
// closes, and the test's folders are removed after it returns.
func (w chatAccessWorld) closeTerminals(t *testing.T, conns ...*websocket.Conn) {
	t.Helper()
	for _, c := range conns {
		_ = c.Close()
	}
	s, err := w.a.Console.Get(context.Background(), w.chat)
	if err != nil {
		return
	}
	if !poll.Until(context.Background(), 5*time.Second, 10*time.Millisecond, func() bool { return s.Viewers() == 0 }) {
		t.Fatal("the chat's terminals did not close")
	}
	// The session's record is written just after the count drops: wait
	// until it holds the last use the closing terminal set, so the write is
	// done before cleanup removes the folder.
	recorded := func() bool {
		raw, err := os.ReadFile(filepath.Join(s.Dir, console.MetaFile))
		var meta console.Meta
		return err == nil && json.Unmarshal(raw, &meta) == nil && meta.LastUsed.Equal(s.LastUsed())
	}
	if !poll.Until(context.Background(), 5*time.Second, 10*time.Millisecond, recorded) {
		t.Fatal("the chat's record was not written after its terminals closed")
	}
}

func chatActs(t *testing.T, a *App, kind string) []audit.Entry {
	t.Helper()
	entries, err := audit.Recent(context.Background(), a.DB, audit.Filter{KindPrefix: kind})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// A chat is not private to whoever opened it: anyone who can read the
// project looks at it (its conversation and who opened it), and only
// someone who may work on changes acts on it. A viewer's say, paste, stop
// and end are refused by authority and reach nothing; an agent credential
// is bounded by its person the same way.
func TestAViewerLooksAtAnotherPersonsChatAndCannotActOnIt(t *testing.T) {
	t.Parallel()
	w := newChatAccessWorld(t)
	h := w.a.Handler()
	base := "/api/v1/console/" + w.chat
	if resp := authzCall(h, "owner", "POST", base+"/say", `{"text":"the owner's words"}`); resp.Code != 200 {
		t.Fatalf("the owner says: %d %s", resp.Code, resp.Body.String())
	}
	if by, may := w.listed(t, "vic"); by != "owner" || may {
		t.Fatalf("the viewer sees the chat opened by %q, may act %v", by, may)
	}
	if by, may := w.listed(t, "jenny"); by != "owner" || !may {
		t.Fatalf("the contributor sees the chat opened by %q, may act %v", by, may)
	}
	if resp := authzCall(h, "vic", "GET", base+"/messages?from=0", ""); resp.Code != 200 || !strings.Contains(resp.Body.String(), "the owner's words") {
		t.Fatalf("the viewer reads the conversation: %d %s", resp.Code, resp.Body.String())
	}
	vicAgent := tokenFor(t, w.a, "vic-agent", "vic", "member", access.TokenAgent)
	acts := []struct{ method, path, body, what string }{
		{"POST", "/say", `{"text":"the viewer's words"}`, "saying something to"},
		{"POST", "/upload", `{"data":"data:image/png;base64,cHVibGlj"}`, "pasting a picture into"},
		{"POST", "/interrupt", `{}`, "stopping"},
		{"DELETE", "", ``, "ending"},
	}
	for _, act := range acts {
		want := "Viewing only: " + act.what + " this chat needs the Project contributor role."
		if resp := authzCall(h, "vic", act.method, base+act.path, act.body); resp.Code != 403 || !strings.Contains(resp.Body.String(), want) || resp.Header().Get("X-Forge-Deny-Code") == "" {
			t.Fatalf("the viewer's %s %s: %d %s", act.method, act.path, resp.Code, resp.Body.String())
		}
		if resp := bearerCall(w.a, vicAgent, act.method, base+act.path, act.body); resp.Code != 403 {
			t.Fatalf("the viewer's agent's %s %s: %d %s", act.method, act.path, resp.Code, resp.Body.String())
		}
	}
	// Each refusal, the person's and their agent's, is kept as an
	// authorization receipt under the viewer.
	refused := 0
	for _, e := range chatActs(t, w.a, "authz.denied") {
		if e.Actor == "vic" && e.Subject == string(access.CapChangeInvokeLane) {
			refused++
		}
	}
	if refused != 2*len(acts) {
		t.Fatalf("the viewer's refusals kept %d receipts, not %d", refused, 2*len(acts))
	}
	s, err := w.a.Console.Get(context.Background(), w.chat)
	if err != nil {
		t.Fatalf("a viewer ended the chat: %v", err)
	}
	if got := s.Messages(); len(got) != 1 || w.agent.Interrupted(w.chat) {
		t.Fatalf("a viewer's acts reached the chat: %+v", got)
	}
	if files, _ := os.ReadDir(filepath.Join(s.Dir, "uploads")); len(files) != 0 {
		t.Fatalf("a viewer pasted into the chat: %v", files)
	}
	// The chat's owner layer refuses whichever door asks.
	if _, err := chatsFor(w.a).Say(asPerson("vic"), s, "through no route"); notRefused(err) {
		t.Fatalf("conversation.Chats let a viewer say: %v", err)
	}
	if err := w.a.changeConsole.EndBy(asPerson("vic"), w.chat); notRefused(err) {
		t.Fatalf("the chat's end let a viewer end it: %v", err)
	}
	// Someone who may work on changes acts on it, and so does their agent.
	jennyAgent := tokenFor(t, w.a, "jenny-agent", "jenny", "member", access.TokenAgent)
	if resp := bearerCall(w.a, jennyAgent, "POST", base+"/say", `{"text":"the contributor's agent's words"}`); resp.Code != 200 {
		t.Fatalf("the contributor's agent says: %d %s", resp.Code, resp.Body.String())
	}
	if resp := authzCall(h, "jenny", "POST", base+"/interrupt", `{}`); resp.Code != 200 || !w.agent.Interrupted(w.chat) {
		t.Fatalf("the contributor stops the turn: %d %s", resp.Code, resp.Body.String())
	}
}

// notRefused is true when err is not a refusal by authority.
func notRefused(err error) bool {
	var denied *access.Denied
	return !errors.As(err, &denied)
}

// Every act on a chat is recorded under the person who did it, with the
// chat's opener beside them, even though the chat runs with its opener's
// authority: "jenny ended owner's chat; its work was saved".
func TestActsOnAnotherMembersChatAreRecordedUnderWhoDidThem(t *testing.T) {
	t.Parallel()
	hw := newHolderWorld(t)
	viewerAndContributor(t, hw.a)
	chat, code, body := hw.openChat(t)
	if code != 201 {
		t.Fatalf("the owner's change chat: %d %s", code, body)
	}
	h := hw.a.Handler()
	if resp := authzCall(h, "jenny", "POST", "/api/v1/console/"+chat+"/say", `{"text":"add a note"}`); resp.Code != 200 {
		t.Fatalf("jenny says: %d %s", resp.Code, resp.Body.String())
	}
	if err := os.WriteFile(filepath.Join(hw.folder, "note.txt"), []byte("jenny asked for this\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, hw.folder, "add", "note.txt")
	gittest.Run(t, hw.folder, "-c", "user.name=agent", "-c", "user.email=agent@forge.invalid", "commit", "-q", "-m", "A note")
	if resp := authzCall(h, "jenny", "DELETE", "/api/v1/console/"+chat, ""); resp.Code != 200 {
		t.Fatalf("jenny ends the owner's chat: %d %s", resp.Code, resp.Body.String())
	}
	for kind, want := range map[string]string{"console.say": "", "console.end": repositoryedit.ChatSaved} {
		entries := chatActs(t, hw.a, kind)
		if len(entries) != 1 || entries[0].Actor != "jenny" || entries[0].Subject != chat {
			t.Fatalf("%s is recorded as %+v", kind, entries)
		}
		var payload struct{ Opener, Saved string }
		if err := json.Unmarshal(entries[0].Payload, &payload); err != nil || payload.Opener != "owner" || payload.Saved != want {
			t.Fatalf("%s names the opener and the save: %s %v", kind, entries[0].Payload, err)
		}
	}
	if got := gittest.Run(t, hw.a.projectRepoPath("game"), "show", hw.change.Branch+":note.txt"); got != "jenny asked for this" {
		t.Fatalf("the ended chat's work was not saved: %q", got)
	}
}

// The terminal: someone who may act types into it; a viewer watches its
// screen, and their keystrokes go nowhere while the screen stays open.
// Watching a chat that is not running does not start it.
func TestAViewerWatchesAChatsTerminalWithoutTyping(t *testing.T) {
	t.Parallel()
	w := newChatAccessWorld(t)
	srv := httptest.NewServer(w.a.Handler())
	defer srv.Close()
	dial := func(who string) (*websocket.Conn, *http.Response, error) {
		u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/console/" + w.chat + "/pty"
		return websocket.DefaultDialer.Dial(u, http.Header{"Origin": {srv.URL}, "X-Forge-Actor": {who}})
	}
	read := func(c *websocket.Conn) string {
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, b, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("reading the screen: %v", err)
		}
		return string(b)
	}
	w.agent.Screen(w.chat, []byte("screen"))
	watcher, _, err := dial("vic")
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	if got := read(watcher); got != "screen" {
		t.Fatalf("the viewer sees %q", got)
	}
	if err := watcher.WriteMessage(websocket.BinaryMessage, []byte("typed by vic")); err != nil {
		t.Fatal(err)
	}
	typist, _, err := dial("jenny")
	if err != nil {
		t.Fatal(err)
	}
	defer w.closeTerminals(t, typist)
	if got := read(typist); got != "screen" {
		t.Fatalf("the viewer's keystrokes reached the screen: %q", got)
	}
	if err := typist.WriteMessage(websocket.BinaryMessage, []byte("+jenny")); err != nil {
		t.Fatal(err)
	}
	if got := read(watcher); got != "+jenny" {
		t.Fatalf("the watcher's screen stays open and live: %q", got)
	}
	if entries := chatActs(t, w.a, "console.terminal"); len(entries) != 1 || entries[0].Actor != "jenny" {
		t.Fatalf("typing into the terminal is recorded under the typist: %+v", entries)
	}
	if err := w.a.Console.Stop(context.Background(), w.chat); err != nil {
		t.Fatal(err)
	}
	if _, resp, err := dial("vic"); err == nil || resp == nil || resp.StatusCode != http.StatusConflict || w.agent.Running(w.chat) {
		t.Fatalf("watching a stopped chat: %v %v, running %v", err, resp, w.agent.Running(w.chat))
	}
}

// Reading a chat never wakes it, whoever reads: a stopped chat wakes when
// someone acts on it, and that act is recorded under them.
func TestReadingAChatNeverWakesIt(t *testing.T) {
	t.Parallel()
	w := newChatAccessWorld(t)
	if err := w.a.Console.Stop(context.Background(), w.chat); err != nil {
		t.Fatal(err)
	}
	h := w.a.Handler()
	path := "/api/v1/console/" + w.chat + "/messages?from=0"
	for _, who := range []string{"vic", "jenny", "owner"} {
		if resp := authzCall(h, who, "GET", path, ""); resp.Code != 200 {
			t.Fatalf("%s reads: %d %s", who, resp.Code, resp.Body.String())
		}
	}
	// A read is done when it answers: the console reads a chat's messages
	// and state in the request, and starts nothing beside it. So the chat
	// is judged after the reads.
	if w.agent.Running(w.chat) {
		t.Fatal("reading the chat woke it")
	}
	if resp := authzCall(h, "jenny", "POST", "/api/v1/console/"+w.chat+"/say", `{"text":"wake up"}`); resp.Code != 200 || !w.agent.Running(w.chat) {
		t.Fatalf("jenny's message wakes the chat: %d %s", resp.Code, resp.Body.String())
	}
	if entries := chatActs(t, w.a, "console.say"); len(entries) != 1 || entries[0].Actor != "jenny" {
		t.Fatalf("the waking act is recorded under jenny: %+v", entries)
	}
}

// Someone who can read the project, however they hold that, looks at its
// chats: an instance auditor with no place on the project's roster lists
// them, reads the conversation and watches the terminal, and acting on one
// is refused as viewing only.
func TestAnyProjectReaderLooksAtItsChats(t *testing.T) {
	t.Parallel()
	w := newChatAccessWorld(t)
	closeTransparentReads(t, w.a)
	if _, err := w.a.Access.EnsureUser(context.Background(), "auditor"); err != nil {
		t.Fatal(err)
	}
	if w.a.addGrant("user", "auditor", access.RoleAuditor, "", access.ScopeInstance, "", "fixture", "boss", "").IsZero() {
		t.Fatal("granting the auditor")
	}
	h := w.a.Handler()
	if resp := authzCall(h, "owner", "POST", "/api/v1/console/"+w.chat+"/say", `{"text":"for the record"}`); resp.Code != 200 {
		t.Fatalf("the owner says: %d %s", resp.Code, resp.Body.String())
	}
	if by, may := w.listed(t, "auditor"); by != "owner" || may {
		t.Fatalf("the auditor sees the chat opened by %q, may act %v", by, may)
	}
	if resp := authzCall(h, "auditor", "GET", "/api/v1/console/"+w.chat+"/messages?from=0", ""); resp.Code != 200 || !strings.Contains(resp.Body.String(), "for the record") {
		t.Fatalf("the auditor reads the conversation: %d %s", resp.Code, resp.Body.String())
	}
	resp := authzCall(h, "auditor", "POST", "/api/v1/console/"+w.chat+"/say", `{"text":"the auditor's words"}`)
	if resp.Code != 403 || !strings.Contains(resp.Body.String(), "Viewing only: saying something to this chat") {
		t.Fatalf("the auditor's message: %d %s", resp.Code, resp.Body.String())
	}
	// Someone with no read of the project sees none of it.
	if resp := authzCall(h, "outsider", "GET", "/api/v1/console/"+w.chat+"/messages?from=0", ""); resp.Code != 403 || strings.Contains(resp.Body.String(), "for the record") {
		t.Fatalf("an outsider reads the chat: %d %s", resp.Code, resp.Body.String())
	}
}

// A terminal opened to type keeps asking: once its typist is no longer
// allowed to act on the chat (made a viewer while the page stays open),
// what they type goes nowhere and the screen stays open to watch.
func TestATerminalStopsTakingInputWhenItsTypistLosesTheRole(t *testing.T) {
	t.Parallel()
	w := newChatAccessWorld(t)
	srv := httptest.NewServer(w.a.Handler())
	defer srv.Close()
	w.agent.Screen(w.chat, []byte("screen"))
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/console/" + w.chat + "/pty"
	conn, _, err := websocket.DefaultDialer.Dial(u, http.Header{"Origin": {srv.URL}, "X-Forge-Actor": {"jenny"}})
	if err != nil {
		t.Fatal(err)
	}
	defer w.closeTerminals(t, conn)
	read := func() (string, error) {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, b, err := conn.ReadMessage()
		return string(b), err
	}
	if got, err := read(); err != nil || got != "screen" {
		t.Fatalf("jenny sees %q %v", got, err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("+typed")); err != nil {
		t.Fatal(err)
	}
	if got, err := read(); err != nil || got != "+typed" {
		t.Fatalf("a contributor types: %q %v", got, err)
	}
	if _, err := w.a.Access.RevokeProjectRoles(context.Background(), "user", "jenny", idstest.N(1), "boss"); err != nil {
		t.Fatal(err)
	}
	if w.a.addGrant("user", "jenny", access.RoleProjectViewer, "", access.ScopeProject, idstest.N(1).String(), "fixture", "boss", "").IsZero() {
		t.Fatal("granting jenny viewer")
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("+after")); err != nil {
		t.Fatal(err)
	}
	// The screen is still live for her: the owner's typing shows; hers did not.
	w.agent.Screen(w.chat, []byte("+owner"))
	if got, err := read(); err != nil || got != "+owner" {
		t.Fatalf("after losing the role jenny's screen shows %q (%v)", got, err)
	}
}

// A chat's own credential acts on its own chat and on no other, even in
// its own project and with its person's authority behind it.
func TestAChatsCredentialActsOnlyOnItsOwnChat(t *testing.T) {
	t.Parallel()
	agent := consoletest.NewAgent()
	a := authzFixture(t, Config{AuthMode: "internal", DockerClient: newFakeDocker(), ConsoleCLI: fixtureCLI(t), ConsoleAgent: agent,
		InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
	configureConsoleModel(t, a)
	open := func() *console.Session {
		meta := consoleMetaFor("", idstest.N(1), ids.Nil)
		s, err := a.Console.Open(t.Context(), console.OpenRequest{Scope: meta.Scope, Labels: meta.Labels, Actor: "owner", Model: "fake/model",
			Workspace: console.Workspace{Path: t.TempDir()}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = a.Console.End(context.Background(), s.ID) })
		return s
	}
	mine, other := open(), open()
	h := a.Handler()
	say := func(sid, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/console/"+sid+"/say", strings.NewReader(`{"text":"from a chat"}`))
		r.Header.Set("X-Forge-Session-Token", token)
		r.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		h.ServeHTTP(resp, r)
		return resp
	}
	if resp := say(other.ID, mine.Token); resp.Code != 403 || len(other.Messages()) != 0 {
		t.Fatalf("a chat said into another: %d %s", resp.Code, resp.Body.String())
	}
	r := httptest.NewRequest("GET", "/api/v1/console/"+other.ID+"/messages?from=0", nil)
	r.Header.Set("X-Forge-Session-Token", mine.Token)
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, r)
	if resp.Code != 403 {
		t.Fatalf("a chat read another: %d %s", resp.Code, resp.Body.String())
	}
	if resp := say(mine.ID, mine.Token); resp.Code != 200 {
		t.Fatalf("a chat says into itself: %d %s", resp.Code, resp.Body.String())
	}
}

// A chat's credential, bound to its project, reads no eval on the HTTP
// routes, though every signed-in person may (E2B-01): evals span
// projects, and a project's instrument reads only its own.
func TestAChatsCredentialReadsNoEval(t *testing.T) {
	t.Parallel()
	agent := consoletest.NewAgent()
	a := authzFixture(t, Config{AuthMode: "internal", DockerClient: newFakeDocker(), ConsoleCLI: fixtureCLI(t), ConsoleAgent: agent,
		InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
	configureConsoleModel(t, a)
	meta := consoleMetaFor("", idstest.N(1), ids.Nil)
	s, err := a.Console.Open(t.Context(), console.OpenRequest{Scope: meta.Scope, Labels: meta.Labels, Actor: "owner", Model: "fake/model",
		Workspace: console.Workspace{Path: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = a.Console.End(context.Background(), s.ID) })
	for _, path := range []string{"/api/v1/evals", "/api/v1/evals/00000000-0001-7000-8000-000000000000"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("X-Forge-Session-Token", s.Token)
		resp := httptest.NewRecorder()
		a.Handler().ServeHTTP(resp, r)
		if resp.Code != 403 {
			t.Fatalf("a chat's credential read %s: %d %s", path, resp.Code, resp.Body.String())
		}
	}
}

// The first words said in a change's chat name a change whose title waits
// for them, and that naming's receipt carries the forge's clock: with the
// forge's clock a year behind, change.titled is stamped in that year, as
// the say beside it is.
func TestAChatsFirstWordsNameItsChangeByTheForgesClock(t *testing.T) {
	t.Parallel()
	hw := newHolderWorld(t)
	moveClock(hw.a, -365*24*time.Hour)
	waiting, err := hw.a.Work.Create(asPerson("owner"), workflow.ChangeDraft{ProjectID: idstest.N(1), Title: "waiting", TitleFromChat: true})
	if err != nil {
		t.Fatal(err)
	}
	hw.change = waiting
	chat, code, body := hw.openChat(t)
	if code != 201 {
		t.Fatalf("the change's chat: %d %s", code, body)
	}
	t.Cleanup(func() { _, _ = hw.a.Console.End(context.Background(), chat) })
	if resp := authzCall(hw.a.Handler(), "owner", "POST", "/api/v1/console/"+chat+"/say", `{"text":"Build a game"}`); resp.Code != 200 {
		t.Fatalf("the owner says: %d %s", resp.Code, resp.Body.String())
	}
	if named, err := hw.a.Work.Change(asPerson("owner"), waiting.ID); err != nil || named.Title != "Build a game" {
		t.Fatalf("the change was not named by its chat: %+v %v", named, err)
	}
	titled := chatActs(t, hw.a, "change.titled")
	if len(titled) != 1 {
		t.Fatalf("change.titled receipts: %+v", titled)
	}
	at, err := time.Parse(time.RFC3339Nano, titled[0].At)
	if err != nil {
		t.Fatal(err)
	}
	if forge := hw.a.cfg.clock.Now(); at.Sub(forge).Abs() > time.Hour {
		t.Fatalf("change.titled stamped %v, AppTown's clock says %v", at, forge)
	}
}
