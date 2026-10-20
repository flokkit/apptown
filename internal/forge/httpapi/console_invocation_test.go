package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/forge/changeactions"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/workflow"
)

func TestConsoleInvokeHTTPBindsWorkAndProvenanceToItsAuthenticatedSession(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	sessions, _, session, _ := consoleHTTPFixture(t, map[string]string{conversation.ProjectLabel: fmt.Sprint(f.ProjectID), conversation.SlugLabel: "shop"})
	other, err := sessions.Open(t.Context(), console.OpenRequest{Scope: "other-chat", Actor: "bls", Model: "fake/model", Workspace: console.Workspace{Path: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sessions.End(context.Background(), other.ID) })
	h := NewConsoleInvocation(sessions, f.Projects, nil, f.Work, changeactions.New(f.Work, nil, f.DB), f.DB, "forge.example", nil)
	call := func(sid, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body)).WithContext(f.Context)
		r.SetPathValue("sid", sid)
		r.Header.Set("X-Forge-Session-Token", token)
		r.Header.Set("X-Forge-Actor", "apptownd-operator")
		w := httptest.NewRecorder()
		h.ConsoleInvoke(w, r)
		return w
	}
	const valid = `{"title":"Keyboard controls","workflow":"implementation","intent":"Keep these exact words.\nSupport the arrow keys.","projectId":999999,"project":"elsewhere"}`
	for _, tc := range []struct {
		name, sid, token, body string
		status                 int
	}{
		{"missing token", session.ID, "", valid, 403},
		{"invalid token", session.ID, "invalid", valid, 403},
		{"other session token", session.ID, other.Token, valid, 403},
		{"unknown session", "missing", session.Token, valid, 404},
		{"malformed JSON", session.ID, session.Token, `{"title":`, 400},
		{"missing title", session.ID, session.Token, `{"workflow":"implementation"}`, 400},
		{"missing workflow", session.ID, session.Token, `{"title":"Keyboard controls"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := call(tc.sid, tc.token, tc.body)
			if w.Code != tc.status {
				t.Fatalf("invoke: %d %s", w.Code, w.Body.String())
			}
			changes, err := f.Work.Changes(f.Context, f.ProjectID, workflow.ChangeList{})
			if err != nil || len(changes) != 1 || changes[0].ID != f.ChangeID || len(conversation.Started(session)) != 0 {
				t.Fatalf("rejected invoke admitted work: %+v %v", changes, err)
			}
		})
	}
	w := call(session.ID, session.Token, valid)
	var out struct {
		Change workflow.Change
		Run    workflow.Run
		URL    string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 201 {
		t.Fatalf("admission: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	const intent = "Keep these exact words.\nSupport the arrow keys."
	if out.Change.ID.IsZero() || out.Change.ID == f.ChangeID || out.Change.ProjectID != f.ProjectID || out.Change.Title != "Keyboard controls" || out.Change.Body != intent || out.Change.Type != "feature" || out.Run.ID.IsZero() || out.Run.ChangeID != out.Change.ID || out.Run.ProjectID != f.ProjectID || out.Run.WorkflowName != "implementation" || out.URL != fmt.Sprintf("https://forge.example/changes/%d", out.Change.ID) {
		t.Fatalf("wrong admitted work or link: %+v", out)
	}
	if ids := conversation.Started(session); len(ids) != 1 || ids[0] != out.Change.ID {
		t.Fatalf("session lost its admitted work: %v", ids)
	}
	snapshot, err := f.Work.Snapshot(f.Context, out.Run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var intents, invocations int
	for _, artifact := range snapshot.Artifacts {
		if artifact.Kind == "intent" {
			intents++
			if artifact.Body != intent {
				t.Fatalf("intent altered: %+v", artifact)
			}
		}
	}
	for _, event := range snapshot.Events {
		if event.Kind == "run.invoked" {
			invocations++
			var receipt struct{ Actor, Via string }
			if err := json.Unmarshal(event.Payload, &receipt); err != nil || receipt.Actor != "bls" || receipt.Via != "console/"+session.ID {
				t.Fatalf("invocation attribution: %s (%v)", event.Payload, err)
			}
		}
	}
	if intents != 1 || invocations != 1 {
		t.Fatalf("admission provenance: intents=%d invocations=%d", intents, invocations)
	}
	if _, err := sessions.End(t.Context(), session.ID); err != nil {
		t.Fatal(err)
	}
	w = call(session.ID, session.Token, valid)
	changes, err := f.Work.Changes(f.Context, f.ProjectID, workflow.ChangeList{})
	if w.Code != 404 || err != nil || len(changes) != 2 {
		t.Fatalf("ended session invoked new work: %d %s changes=%+v (%v)", w.Code, w.Body.String(), changes, err)
	}
}

func TestConsoleInvokeHTTPChangeChatCannotCreateSiblingWork(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	sessions, _, session, _ := consoleHTTPFixture(t, map[string]string{conversation.ProjectLabel: fmt.Sprint(f.ProjectID), conversation.SlugLabel: "shop", conversation.ChangeLabel: fmt.Sprint(f.ChangeID)})
	h := NewConsoleInvocation(sessions, f.Projects, nil, f.Work, changeactions.New(f.Work, nil, f.DB), f.DB, "forge.example", nil)
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"title":"Escape the chat","workflow":"implementation"}`)).WithContext(f.Context)
	r.SetPathValue("sid", session.ID)
	r.Header.Set("X-Forge-Session-Token", session.Token)
	w := httptest.NewRecorder()
	h.ConsoleInvoke(w, r)
	if w.Code != 403 || !strings.Contains(w.Body.String(), fmt.Sprintf("change #%d", f.ChangeID)) {
		t.Fatalf("change chat escape: %d %s", w.Code, w.Body.String())
	}
	changes, err := f.Work.Changes(f.Context, f.ProjectID, workflow.ChangeList{})
	if err != nil || len(changes) != 1 || changes[0].ID != f.ChangeID || len(conversation.Started(session)) != 0 {
		t.Fatalf("change chat created sibling: %+v %v", changes, err)
	}
}
