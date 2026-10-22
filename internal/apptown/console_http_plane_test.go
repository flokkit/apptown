package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestGETOnly(t *testing.T) {
	t.Parallel()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	srv := httptest.NewServer(GETOnly(inner))
	defer srv.Close()
	check := func(method string, path string, want int) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s %s = %d, want %d", method, path, resp.StatusCode, want)
		}
	}
	check("GET", "/api/v1/projects", 200)
	check("POST", "/api/v1/projects", http.StatusMethodNotAllowed)
	check("DELETE", "/api/v1/projects/x", http.StatusMethodNotAllowed)
	// The two sanctioned mutations: invoke (credential-gated inside) and
	// complaints (inert by design).
	check("POST", "/api/v1/console/00000000-0001-7000-8000-000000000000/invoke", 200)
	check("POST", "/api/v1/complaints", 200)
	// A git fetch's pack request is a POST that only reads. A push's passes
	// for the git endpoint to refuse all but a step's scratch push
	// (TestAStepPushesOnlyItsOwnScratchRef); nothing else under /git does.
	check("POST", "/git/app/git-upload-pack", 200)
	check("POST", "/git/app/git-receive-pack", 200)
	check("POST", "/git/app/git-upload-pack/x", http.StatusMethodNotAllowed)
	check("POST", "/git/app/git-receive-pack/x", http.StatusMethodNotAllowed)
	check("POST", "/git/app/objects/info/packs", http.StatusMethodNotAllowed)
	// Git sends no service query with its POSTs; one that names a service,
	// the other one or its own, is not let through.
	check("POST", "/git/app/git-upload-pack?service=git-receive-pack", http.StatusMethodNotAllowed)
	check("POST", "/git/app/git-receive-pack?service=git-upload-pack", http.StatusMethodNotAllowed)
	check("POST", "/git/app/git-upload-pack?service=git-upload-pack", http.StatusMethodNotAllowed)
	// The PTY is a GET but an input path — never through this plane.
	check("GET", "/api/v1/console/abc123/pty", http.StatusMethodNotAllowed)
}

func TestConsoleActuatorPlaneAuthenticatesInvocationAndKeepsProjectScope(t *testing.T) {
	t.Parallel()
	a := authzFixtureWithApproval(t, workflow.ApprovalConfig{}, Config{AuthMode: "internal", PublicHost: "forge.example",
		DockerClient: newFakeDocker(), ConsoleCLI: fixtureCLI(t), ConsoleAgent: consoletest.NewAgent(),
		InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
	configureConsoleModel(t, a)
	session, err := a.Console.Open(t.Context(), console.OpenRequest{
		Scope: conversation.Key(idstest.N(1), ids.Nil), Labels: map[string]string{conversation.ProjectLabel: idstest.N(1).String(), conversation.SlugLabel: "game"},
		Actor: "owner", Model: "fake/model", Workspace: console.Workspace{Path: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := a.Console.Open(t.Context(), console.OpenRequest{
		Scope: conversation.Key(idstest.N(2), ids.Nil), Labels: map[string]string{conversation.ProjectLabel: idstest.N(2).String(), conversation.SlugLabel: "open"},
		Actor: "outsider", Model: "fake/model", Workspace: console.Workspace{Path: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := GETOnly(a.Handler())
	path := "/api/v1/console/" + session.ID + "/invoke"
	call := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(`{"title":"Through the agent port","workflow":"implement","projectId":"00000000-0002-7000-8000-000000000000"}`))
		r.Header.Set("X-AppTown-Session-Token", token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {other.Token, 403}} {
		w := call(tc.token)
		if w.Code != tc.status {
			t.Fatalf("invalid scope: %d %s", w.Code, w.Body.String())
		}
		changes, err := a.Work.Changes(asPerson("owner"), idstest.N(1), workflow.ChangeList{})
		if err != nil || len(changes) != 1 {
			t.Fatalf("denial admitted work: %+v %v", changes, err)
		}
	}
	w := call(session.Token)
	var out struct {
		Change workflow.Change
		Run    workflow.Run
		URL    string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 201 || out.Change.ID.IsZero() || out.Change.ProjectID != idstest.N(1) || out.Run.ChangeID != out.Change.ID || out.Run.ProjectID != idstest.N(1) || !strings.HasSuffix(out.URL, fmt.Sprintf("/changes/%d", out.Change.ID)) {
		t.Fatalf("actuator invoke: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	a.Work.Wait()
	// A session token does not make the broader mutation API available here.
	r := httptest.NewRequest("DELETE", fmt.Sprintf("/api/v1/console/%s", session.ID), nil)
	r.Header.Set("X-AppTown-Session-Token", session.Token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("access plane allowed deletion: %d %s", w.Code, w.Body.String())
	}
	if _, err := a.Console.Get(context.Background(), session.ID); err != nil {
		t.Fatalf("blocked deletion ended session: %v", err)
	}
}
