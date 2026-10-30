package apptown

import (
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/status"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/conversation"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestConsoleChangesUsesCurrentBatchedActivityAndPreservesHistory(t *testing.T) {
	t.Parallel()
	a, pid := tv_ov_seed(t)
	changes, err := a.Work.Changes(asPerson("bls"), pid, workflow.ChangeList{})
	if err != nil || len(changes) != 3 {
		t.Fatalf("overview fixture changes: %+v %v", changes, err)
	}
	var active, shipped workflow.Change
	for _, change := range changes {
		switch change.Title {
		case "in flight":
			active = change.Change
		case "shipped one":
			shipped = change.Change
		}
	}
	if active.ID.IsZero() || shipped.ID.IsZero() || active.RunID.IsZero() || shipped.State != workflow.ChangeShipped {
		t.Fatalf("console history prerequisites: %+v %+v", active, shipped)
	}
	session := a.Console.Restore(withLabel(consoleMetaFor("long-lived", pid, ids.Nil), conversation.StartedLabel, fmt.Sprintf("%s,%s,%s", active.ID, shipped.ID, idstest.N(99999))))
	get := func() *httptest.ResponseRecorder {
		out := httptest.NewRecorder()
		req := testRequestAs(httptest.NewRequest("GET", "/console/long-lived/changes", nil), "bls")
		req.SetPathValue("sid", session.ID)
		httpapi.NewConsoleChanges(a.Console, a.Work, a.changeActions, a.DB).ConsoleChanges(out, req.WithContext(callerContext(req)))
		return out
	}
	out := get()
	var history httpapi.ConsoleChangeList
	if err := json.Unmarshal(out.Body.Bytes(), &history); err != nil || out.Code != 200 || len(history.Changes) != 2 {
		t.Fatalf("console history: %d %s (%v)", out.Code, out.Body.String(), err)
	}
	if history.Changes[0].ChangeID != active.ID || history.Changes[0].Doing != "waiting on you" || history.Changes[0].StatusLabel != status.DisplayWaitingForYou ||
		history.Changes[1].ChangeID != shipped.ID || history.Changes[1].State != workflow.ChangeShipped || history.Changes[1].StateLabel != "shipped" || history.Changes[1].StatusLabel != status.DisplayFinished || history.Changes[1].Doing != "" {
		t.Fatalf("console labels changed lifecycle or hid waiting party: %s", out.Body.String())
	}
	dbtest.FailNextStatement(t, a.DB, "WITH RECURSIVE activity_tree")
	if out := get(); out.Code != 500 {
		t.Fatalf("console hid failed activity read: %d %s", out.Code, out.Body.String())
	}
	if _, err := a.Work.Abandon(asPerson("bls"), workflow.StopWork{RunID: active.RunID, Reason: "work stopped while chat remains open"}); err != nil {
		t.Fatal(err)
	}
	out = get()
	if out.Code != 200 || strings.Contains(out.Body.String(), "waiting on you") || len(conversation.Started(session)) != 3 {
		t.Fatalf("long-lived console did not observe new state: %d %s", out.Code, out.Body.String())
	}
}

func TestConsoleWorkReadsStayInSessionProject(t *testing.T) {
	t.Parallel()
	a := testApp(t)

	testAdministrator(t, a, "boss")
	project := createReadTestProject(t, a, "chat")
	other := createReadTestProject(t, a, "elsewhere")
	local := createReadTestChange(t, a, project, "local history")
	foreign := createReadTestChange(t, a, other, "foreign history")
	sm := withLabel(consoleMetaFor("scoped", project, foreign), conversation.StartedLabel, fmt.Sprintf("%d,%d", local, foreign))
	sm.Actor, sm.Token = "boss", "secret"
	session := a.Console.Restore(sm)
	request := func(method, path string) *httptest.ResponseRecorder {
		r := testRequestAs(httptest.NewRequest(method, "/api/v1/console/scoped/"+path, strings.NewReader(`{}`)), "anonymous")
		r.Header.Set("X-AppTown-Actor", "boss")
		r.Header.Set("X-AppTown-Session-Token", "secret")
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		return w
	}
	// Even this administrator's broad visibility cannot widen the chat history.
	if w := request("GET", "changes"); w.Code != 200 || !strings.Contains(w.Body.String(), "local history") || strings.Contains(w.Body.String(), "foreign history") {
		t.Fatalf("session history scope: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"preview", "land", "recheck"} {
		if w := request("POST", path); w.Code != 403 {
			t.Fatalf("%s crossed the session project: %d %s", path, w.Code, w.Body.String())
		}
	}
	if len(conversation.Started(session)) != 2 {
		t.Fatal("observation rewrote historical session metadata")
	}
}
