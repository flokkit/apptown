package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
)

func consoleHTTPFixture(t *testing.T, labels map[string]string) (*console.Service, *consoletest.Agent, *console.Session, ConsoleMessages) {
	t.Helper()
	agent := consoletest.NewAgent()
	dir := t.TempDir()
	sessions := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: dir}})
	session, err := sessions.Open(t.Context(), console.OpenRequest{Scope: "http-test", Labels: labels, Workspace: console.Workspace{Path: t.TempDir()}, Actor: "bls", Model: "fake/model"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sessions.End(context.Background(), session.ID) })
	db := databaseSuite.New(t)
	chats := conversation.Chats{Sessions: sessions, Authority: everyoneActs{}, DB: db}
	work := workflow.New(context.Background(), db, workflow.Dependencies{})
	t.Cleanup(work.Stop)
	return sessions, agent, session, NewConsoleMessages(chats, work, db, dir, nil)
}

// everyoneActs lets every caller act on a chat: these tests are about the
// handlers; who may act is conversation.Chats' and the app's routes' tests.
type everyoneActs struct{}

func (everyoneActs) Require(context.Context, access.Permission) (access.Caller, error) {
	return access.Caller{Receipt: "bls"}, nil
}

func consoleHTTPRequest(handler http.HandlerFunc, sid, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.SetPathValue("sid", sid)
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func TestConsoleMessageHTTPDeliveryPollingAndInterrupt(t *testing.T) {
	t.Parallel()
	_, agent, session, h := consoleHTTPFixture(t, nil)
	for _, body := range []string{`{"text":`, `{"text":4}`, `{"text":" \n "}`} {
		w := consoleHTTPRequest(h.ConsoleSay, session.ID, "POST", "/", body)
		if w.Code != 400 {
			t.Fatalf("invalid message accepted: %d %s", w.Code, w.Body.String())
		}
	}
	if got := session.Messages(); len(got) != 0 {
		t.Fatalf("invalid requests reached the agent: %+v", got)
	}
	w := consoleHTTPRequest(h.ConsoleSay, session.ID, "POST", "/", `{"text":"Please keep\nthese words."}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("send: %d %s", w.Code, w.Body.String())
	}
	agent.Reply(session.ID, "Done.")
	w = consoleHTTPRequest(h.ConsoleMessages, session.ID, "GET", "/?from=0", "")
	var page struct {
		Messages []console.Message `json:"messages"`
		Turn     string            `json:"turn"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Messages) != 2 {
		t.Fatalf("poll: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	if page.Messages[0].Text != "Please keep these words." || page.Messages[1].Text != "Done." {
		t.Fatalf("unexpected delivered text: %+v", page.Messages)
	}
	w = consoleHTTPRequest(h.ConsoleMessages, session.ID, "GET", "/?from=1", "")
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Messages) != 1 || page.Messages[0].Text != "Done." {
		t.Fatalf("incremental poll replayed history: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	if w := consoleHTTPRequest(h.ConsoleMessages, session.ID, "GET", "/?from=not-a-number", ""); w.Code != 400 {
		t.Fatalf("bad cursor: %d %s", w.Code, w.Body.String())
	}
	w = consoleHTTPRequest(h.ConsoleInterrupt, session.ID, "POST", "/", `{}`)
	if w.Code != 200 || !agent.Interrupted(session.ID) {
		t.Fatalf("interrupt did not reach the session: %d %s", w.Code, w.Body.String())
	}
}

func TestConsoleMessageHTTPKeepsSuccessfulDeliveryWhenNamingFails(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	change, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "Awaiting a message", TitleFromChat: true})
	if err != nil {
		t.Fatal(err)
	}
	_, _, session, h := consoleHTTPFixture(t, map[string]string{conversation.ProjectLabel: fmt.Sprint(f.ProjectID), conversation.ChangeLabel: fmt.Sprint(change.ID)})
	h.db = f.DB
	t.Cleanup(f.DB.FailNextStatement("-- name: EditChangeTitle :execrows"))
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"text":"Build a little game"}`)).WithContext(f.Context)
	r.SetPathValue("sid", session.ID)
	w := httptest.NewRecorder()
	h.ConsoleSay(w, r)
	if w.Code != 200 || len(session.Messages()) != 1 || session.Messages()[0].Text != "Build a little game" {
		t.Fatalf("naming failure encouraged duplicate delivery: %d %s", w.Code, w.Body.String())
	}
	stored, err := f.Work.Change(f.Context, change.ID)
	if err != nil || stored.Title != "Awaiting a message" {
		t.Fatalf("naming failure was not exercised: %+v %v", stored, err)
	}
}

func TestConsoleMessageHTTPRejectsEndedAndUnknownSessions(t *testing.T) {
	t.Parallel()
	sessions, agent, session, h := consoleHTTPFixture(t, nil)
	if _, err := sessions.End(t.Context(), session.ID); err != nil {
		t.Fatal(err)
	}
	for _, sid := range []string{session.ID, "missing"} {
		for _, handler := range []http.HandlerFunc{h.ConsoleSay, h.ConsoleInterrupt, h.ConsoleMessages, h.ConsoleUpload} {
			w := consoleHTTPRequest(handler, sid, "POST", "/", `{"text":"do not deliver"}`)
			if w.Code != 404 {
				t.Fatalf("unavailable session: %d %s", w.Code, w.Body.String())
			}
		}
	}
	if agent.Running(session.ID) || agent.Interrupted(session.ID) || len(session.Messages()) != 0 {
		t.Fatal("unavailable session was revived or received an operation")
	}
}

func TestConsoleUploadHTTPValidatesAndServesUntrustedBytes(t *testing.T) {
	t.Parallel()
	_, _, session, h := consoleHTTPFixture(t, nil)
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"data":`, 400},
		{`{"data":"data:text/html;base64,SGk="}`, 422},
		{`{"data":"data:image/png;base64,not base64"}`, 400},
	} {
		w := consoleHTTPRequest(h.ConsoleUpload, session.ID, "POST", "/", tc.body)
		if w.Code != tc.status {
			t.Fatalf("bad upload: %d %s", w.Code, w.Body.String())
		}
	}
	files, err := os.ReadDir(filepath.Join(session.Dir, "uploads"))
	if err != nil || len(files) != 0 {
		t.Fatalf("invalid upload wrote files: %v %v", files, err)
	}
	// The caller's image label must not turn active content into an inline page.
	content := "<!doctype html><script>alert('upload')</script>"
	body := `{"data":"data:image/png;base64,` + base64.StdEncoding.EncodeToString([]byte(content)) + `"}`
	w := consoleHTTPRequest(h.ConsoleUpload, session.ID, "POST", "/", body)
	var upload struct{ Name, Path string }
	if err := json.Unmarshal(w.Body.Bytes(), &upload); err != nil || w.Code != 200 || upload.Name == "" || upload.Path == "" {
		t.Fatalf("upload: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("sid", session.ID)
	r.SetPathValue("name", upload.Name)
	w = httptest.NewRecorder()
	h.ConsoleUploadGet(w, r)
	if w.Code != 200 || w.Body.String() != content || w.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("untrusted upload served unsafely: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
}

// A chat's view masks known secret values; the record the agent writes, which
// it resumes from, keeps what it saw.
//
// Kills: a view that showed a secret the helper echoed.
func TestAChatsViewMasksASecretItsRecordKeeps(t *testing.T) {
	t.Parallel()
	sessions, agent, session, h := consoleHTTPFixture(t, nil)
	_ = sessions
	h.mask = func(context.Context) func(string) string {
		return func(s string) string { return strings.ReplaceAll(s, "hunter2-long-value", "[secret]") }
	}
	agent.Reply(session.ID, "the token is hunter2-long-value, as asked")
	w := consoleHTTPRequest(h.ConsoleMessages, session.ID, "GET", "/?from=0", "")
	if strings.Contains(w.Body.String(), "hunter2-long-value") || !strings.Contains(w.Body.String(), "the token is [secret], as asked") {
		t.Fatalf("the view: %s", w.Body.String())
	}
	found := false
	_ = filepath.WalkDir(session.Dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), "hunter2-long-value") {
				found = true
			}
		}
		return nil
	})
	if !found {
		t.Fatal("the record lost what the agent saw")
	}
}

// A page of messages is masked from one snapshot of the stores, however
// many texts and tool details it has (review CT-C06).
func TestAPageOfMessagesTakesOneMaskSnapshot(t *testing.T) {
	t.Parallel()
	snapshots := 0
	m := Mask(func(context.Context) func(string) string {
		snapshots++
		return func(s string) string { return s }
	})
	msgs := make([]console.Message, 50)
	for i := range msgs {
		msgs[i].Text = "words"
		msgs[i].Tools = append(msgs[i].Tools, struct {
			Name   string `json:"name"`
			Detail string `json:"detail,omitempty"`
		}{Name: "bash", Detail: "ls"})
	}
	m.Messages(context.Background(), msgs)
	if snapshots != 1 {
		t.Fatalf("%d snapshots for one page", snapshots)
	}
}
