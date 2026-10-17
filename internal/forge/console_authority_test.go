package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/consoletest"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// Project authority (putting an agent to work) gates chat creation before
// any container work happens.
func TestProjectAuthorityGatesConsoleCreate(t *testing.T) {
	t.Parallel()
	a := testApp(t)

	createReadTestProject(t, a, "game")
	h := a.Handler()
	req := httptest.NewRequest("POST", "/api/v1/projects/game/console", strings.NewReader(`{}`))
	req.Header.Set("X-Forge-Actor", "outsider")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "outsider may not start") {
		t.Fatalf("console create must respect project authority: %d %s", w.Code, w.Body.String())
	}
}

// These handlers rely on the assembled router for authorization. Keep the
// real handlers here: a denial must prevent delivery, interruption and writes,
// as well as withholding the conversation and its attachments.
func TestConsoleRoutesIsolateProjectMessagesAndAttachments(t *testing.T) {
	t.Parallel()
	agent := consoletest.NewAgent()
	a := authzFixture(t, Config{DockerClient: newFakeDocker(), ConsoleCLI: fixtureCLI(t), ConsoleAgent: agent,
		InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
	configureConsoleModel(t, a)
	// Reading a chat follows reading its project; with the transparent-reads
	// default in place everyone signed in reads it.
	closeTransparentReads(t, a)
	meta := consoleMetaFor("", idstest.N(1), ids.Nil)
	session, err := a.Console.Open(t.Context(), console.OpenRequest{
		Scope: meta.Scope, Labels: meta.Labels, Actor: "owner", Model: "fake/model",
		Workspace: console.Workspace{Path: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = a.Console.End(context.Background(), session.ID) })
	if err := a.Console.Send(t.Context(), session.ID, "private conversation"); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	base := "/api/v1/console/" + session.ID
	w := authzCall(h, "owner", "POST", base+"/upload", `{"data":"data:image/png;base64,cHJpdmF0ZSBhdHRhY2htZW50"}`)
	var uploaded struct{ Name string }
	if err := json.Unmarshal(w.Body.Bytes(), &uploaded); err != nil || w.Code != 200 || uploaded.Name == "" {
		t.Fatalf("owner upload: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/messages", ""},
		{"GET", "/uploads/" + uploaded.Name, ""},
		{"POST", "/say", `{"text":"unauthorized message"}`},
		{"POST", "/interrupt", `{}`},
		{"POST", "/upload", `{"data":"data:image/png;base64,cHVibGlj"}`},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			// outsider belongs to project 2, not this session's project 1.
			w := authzCall(h, "outsider", tc.method, base+tc.path, tc.body)
			if w.Code != 403 || strings.Contains(w.Body.String(), "private conversation") || strings.Contains(w.Body.String(), "private attachment") {
				t.Fatalf("cross-project request: %d %s", w.Code, w.Body.String())
			}
		})
	}
	if got := session.Messages(); len(got) != 1 || got[0].Text != "private conversation" || agent.Interrupted(session.ID) {
		t.Fatalf("denied requests reached the agent: %+v", got)
	}
	files, err := os.ReadDir(filepath.Join(session.Dir, "uploads"))
	if err != nil || len(files) != 1 || files[0].Name() != uploaded.Name {
		t.Fatalf("denied upload changed attachments: %v %v", files, err)
	}
	// Exercise allowed requests too, so a disconnected route cannot satisfy
	// all the negative assertions above.
	w = authzCall(h, "owner", "POST", base+"/say", `{"text":"authorized message"}`)
	if w.Code != 200 {
		t.Fatalf("owner send: %d %s", w.Code, w.Body.String())
	}
	w = authzCall(h, "owner", "GET", base+"/messages", "")
	var page struct{ Messages []console.Message }
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Messages) != 2 || page.Messages[1].Text != "authorized message" {
		t.Fatalf("owner transcript: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	w = authzCall(h, "owner", "GET", base+"/uploads/"+uploaded.Name, "")
	if w.Code != 200 || w.Body.String() != "private attachment" {
		t.Fatalf("owner attachment: %d %s", w.Code, w.Body.String())
	}
	w = authzCall(h, "owner", "POST", base+"/interrupt", `{}`)
	if w.Code != 200 || !agent.Interrupted(session.ID) {
		t.Fatalf("owner interrupt: %d %s", w.Code, w.Body.String())
	}
}

// A console owner's disabled account must be refused by drafting as it is
// by a direct request through the inference gateway.
func TestDraftingRefusesADisabledConsoleOwner(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"content":[{"type":"text","text":"invalid workflow"}]}`)
	}))
	defer upstream.Close()
	a := testApp(t, Config{AuthMode: "internal", ConsoleCLI: fixtureCLI(t), InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
	if _, err := a.Integrations.Bindings.Save(context.Background(), "native", "model", "pi", mustJSON(map[string]any{"models": []string{"drafter"}, "providerDef": map[string]any{"baseUrl": upstream.URL, "api": "anthropic-messages", "apiKey": "fixture"}}), "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := a.Models.Set(context.Background(), "draft", ids.Nil, "", "standard", "native/drafter", "fixture"); err != nil {
		t.Fatal(err)
	}
	testAdministrator(t, a, "admin")
	m := consoleMetaFor("disabled-owner", ids.Nil, ids.Nil)
	m.Actor, m.Token = "alice", "review-console-token"
	a.Console.Restore(m)
	disabled := true
	if err := a.Access.SaveUser(context.Background(), identity.AccountChange{Username: "alice", Disabled: &disabled}, access.Principal{Kind: "user", Name: "admin", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.inference.Complete(context.Background(), m.Token, "native/drafter", "s", "p"); err == nil || calls.Load() != 0 {
		t.Fatalf("direct inference should refuse: %v calls %d", err, calls.Load())
	}
	r := httptest.NewRequest("POST", "/api/v1/assist/workflow", strings.NewReader(`{"description":"draft a workflow"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forge-Session-Token", m.Token)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if calls.Load() != 0 {
		t.Fatalf("disabled console owner made %d upstream drafting calls: HTTP %d %s", calls.Load(), w.Code, w.Body.String())
	}
}
