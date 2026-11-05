package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"

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
	sessions, agent, session, h := consoleHTTPFixture(t, nil)
	for _, body := range []string{`{"text":`, `{"text":4}`, `{"text":" \n "}`} {
		w := consoleHTTPRequest(h.ConsoleSay, session.ID, "POST", "/", body)
		if w.Code != 400 {
			t.Fatalf("invalid message accepted: %d %s", w.Code, w.Body.String())
		}
	}
	if got := sessionMessages(t, sessions, session); len(got) != 0 {
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
	sessions, _, session, h := consoleHTTPFixture(t, map[string]string{conversation.ProjectLabel: fmt.Sprint(f.ProjectID), conversation.ChangeLabel: fmt.Sprint(change.ID)})
	h.db = f.DB
	t.Cleanup(dbtest.NextStatementFailure(f.DB, "-- name: EditChangeTitle :execrows"))
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"text":"Build a little game"}`)).WithContext(f.Context)
	r.SetPathValue("sid", session.ID)
	w := httptest.NewRecorder()
	h.ConsoleSay(w, r)
	if messages := sessionMessages(t, sessions, session); w.Code != 200 || len(messages) != 1 || messages[0].Text != "Build a little game" {
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
	if agent.Running(session.ID) || agent.Interrupted(session.ID) || len(sessionMessages(t, sessions, session)) != 0 {
		t.Fatal("unavailable session was revived or received an operation")
	}
}

// Refused uploads must not attempt to consume a multipart request body.
func TestConsoleUploadChecksAuthorityBeforeReadingBody(t *testing.T) {
	t.Parallel()
	_, _, session, h := consoleHTTPFixture(t, nil)
	for _, otherChat := range []bool{false, true} {
		t.Run(fmt.Sprint(otherChat), func(t *testing.T) {
			h.chats.Authority = viewingOnlyAuthority{}
			r := httptest.NewRequest("POST", "/", nil)
			r.SetPathValue("sid", session.ID)
			r.Header.Set("Content-Type", "multipart/form-data; boundary=test")
			body := &unreadUploadBody{}
			r.Body = body
			want := "Viewing only: attaching a file to this chat needs the Project contributor role."
			if otherChat {
				r = r.WithContext(access.WithCaller(r.Context(), access.Caller{Principal: access.Principal{Kind: "instrument", Instrument: "console", ConsoleID: "another-chat"}}))
				want = "a chat may only act on its own session"
			}
			w := httptest.NewRecorder()
			h.ConsoleUpload(w, r)
			if w.Code != 403 || !strings.Contains(w.Body.String(), want) || body.read {
				t.Fatalf("authority before body: %d %s; body read: %v", w.Code, w.Body.String(), body.read)
			}
		})
	}
}

type viewingOnlyAuthority struct{}

func (viewingOnlyAuthority) Require(context.Context, access.Permission) (access.Caller, error) {
	return access.Caller{}, &access.Denied{Code: access.DenyMissingCapability}
}

type unreadUploadBody struct{ read bool }

func (b *unreadUploadBody) Read([]byte) (int, error) {
	b.read = true
	return 0, io.EOF
}
func (*unreadUploadBody) Close() error { return nil }

// Uploads retain bytes and safe names, reach the agent by a path outside its
// repository, and remain downloadable from history with untrusted-file headers.
func TestConsoleUploadHTTPValidatesAndServesUntrustedBytes(t *testing.T) {
	t.Parallel()
	sessions, _, session, h := consoleHTTPFixture(t, nil)
	for _, tc := range []struct {
		name, content string
		picture       bool
	}{
		{"../reports/customers.csv", "name,total\nsam,42\n", false},
		{`C:\reports\plan.pdf`, "%PDF-1.7\nexample", false},
		{"cat.png", "\x89PNG\r\n\x1a\n", true},
		{"page.png", "<!doctype html><script>alert(1)</script>", false},
	} {
		w := uploadFile(t, h, session.ID, tc.name, []byte(tc.content))
		var att console.Attachment
		if err := json.Unmarshal(w.Body.Bytes(), &att); err != nil || w.Code != 200 {
			t.Fatalf("upload: %d %s (%v)", w.Code, w.Body.String(), err)
		}
		if !console.ValidAttachmentName(att.Name) || strings.ContainsAny(att.Name, `/\`) || att.Size != int64(len(tc.content)) || att.Picture != tc.picture {
			t.Fatalf("file facts: %+v", att)
		}
		stored, err := os.ReadFile(filepath.Join(session.Dir, "uploads", att.Name))
		if err != nil || string(stored) != tc.content {
			t.Fatalf("stored bytes: %q %v", stored, err)
		}
		w = consoleHTTPRequest(h.ConsoleSay, session.ID, "POST", "/", fmt.Sprintf(`{"attachments":[%q]}`, att.Name))
		if w.Code != 200 {
			t.Fatalf("say: %d %s", w.Code, w.Body.String())
		}
		messages := sessionMessages(t, sessions, session)
		want := fmt.Sprintf("bls attached %s (%d bytes): %s", att.Name, att.Size, att.Path)
		if !strings.Contains(messages[len(messages)-1].Text, want) {
			t.Fatalf("agent never received %q: %+v", want, messages)
		}
		if tc.picture && !strings.Contains(messages[len(messages)-1].Text, "I pasted a picture — look at "+att.Path) {
			t.Fatal("picture instruction lost")
		}
		r := httptest.NewRequest("GET", "/", nil)
		r.SetPathValue("sid", session.ID)
		r.SetPathValue("name", att.Name)
		w = httptest.NewRecorder()
		h.ConsoleUploadGet(w, r)
		disposition := "attachment;"
		if tc.picture {
			disposition = "inline;"
		}
		if w.Code != 200 || w.Body.String() != tc.content || !strings.HasPrefix(w.Header().Get("Content-Disposition"), disposition) || w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("unsafe download: %d %v", w.Code, w.Header())
		}
		path, err := sessions.RecordedAttachment(session.Scope, session.ID, att.Name)
		if err != nil || path != filepath.Join(session.Dir, "uploads", att.Name) {
			t.Fatalf("history cannot serve file: %s %v", path, err)
		}
	}
	first := uploadFile(t, h, session.ID, "customers.csv", []byte("other"))
	if !strings.Contains(first.Body.String(), "customers-2.csv") {
		t.Fatalf("file replaced instead of kept unique: %s", first.Body.String())
	}
	for _, tc := range []struct {
		body   []byte
		status int
		reason string
	}{
		{nil, 400, "empty"},
		{bytes.Repeat([]byte("x"), 26<<20), 413, "25 MB is the cap"},
	} {
		w := uploadFile(t, h, session.ID, "file.bin", tc.body)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.reason) {
			t.Fatalf("refusal: %d %s", w.Code, w.Body.String())
		}
	}
	names := make([]string, 11)
	for i := range names {
		names[i] = "customers.csv"
	}
	raw, _ := json.Marshal(map[string]any{"attachments": names})
	w := consoleHTTPRequest(h.ConsoleSay, session.ID, "POST", "/", string(raw))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "cap is 10") {
		t.Fatalf("message cap: %d %s", w.Code, w.Body.String())
	}
	if _, err := sessions.End(t.Context(), session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.RecordedAttachment(session.Scope, session.ID, "customers.csv"); err != nil {
		t.Fatalf("ended history lost file: %v", err)
	}
}

func uploadFile(t *testing.T, h ConsoleMessages, sid, name string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	multipartBody := multipart.NewWriter(&buf)
	part, err := multipartBody.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(body); err != nil {
		t.Fatal(err)
	}
	multipartBody.Close()
	r := httptest.NewRequest("POST", "/", &buf)
	r.Header.Set("Content-Type", multipartBody.FormDataContentType())
	r.SetPathValue("sid", sid)
	w := httptest.NewRecorder()
	h.ConsoleUpload(w, r)
	return w
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
	m := mask(func(context.Context) func(string) string {
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
	m.messages(context.Background(), msgs)
	if snapshots != 1 {
		t.Fatalf("%d snapshots for one page", snapshots)
	}
}

func sessionMessages(t *testing.T, sessions *console.Service, session *console.Session) []console.Message {
	t.Helper()
	conversations, unreadable, err := sessions.Transcript(t.Context(), session.Scope)
	if err != nil || len(unreadable) != 0 {
		t.Fatalf("read chat transcript: %v; unreadable: %v", err, unreadable)
	}
	for _, conversation := range conversations {
		if conversation.ID == session.ID {
			return conversation.Messages
		}
	}
	return nil
}
