package apptown

import (
	"encoding/json"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestHTTPAuthorityScopeFailureStopsEvenAdministrator(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	for _, tc := range []struct {
		path, method, body, marker string
		skip                       int
	}{
		{"/api/v1/projects/game", "GET", "", "-- name: ProjectByID :one", 0},
		{"/api/v1/projects/game/members", "POST", `{"grantee":"outsider","role":"project-viewer"}`, "-- name: ProjectByID :one", 1},
		// Release admission now resolves scope in the deployment owner, with no prior transport authority read.
		{"/api/v1/projects/game/release", "POST", `{}`, "-- name: ProjectByID :one", 0},
		{"/api/v1/authz/me?project=game", "GET", "", "-- name: ProjectByID :one", 0},
		{"/api/v1/authz/me?project=game", "GET", "", "-- name: ProjectBySlug :one", 0},
	} {
		t.Run(tc.method+tc.path+tc.marker, func(t *testing.T) {
			restore := dbtest.StatementFailureAfter(a.DB, tc.marker, tc.skip)
			w := authzCall(h, "boss", tc.method, tc.path, tc.body)
			restore()
			if w.Code != 503 {
				t.Fatalf("unavailable scope: %d %s", w.Code, w.Body)
			}
		})
	}
	if grants, err := a.Access.GrantsAt(t.Context(), access.ScopeProject, idstest.N(1).String()); err != nil {
		t.Fatal(err)
	} else {
		for _, g := range grants {
			if g.Subject == "outsider" {
				t.Fatalf("scope failure granted outsider access: %+v", g)
			}
		}
	}
	if w := authzCall(h, "owner", "GET", "/api/v1/projects/game", ""); w.Code != 200 {
		t.Fatalf("scoped owner lost access: %d %s", w.Code, w.Body)
	}
}

func TestGrantCleanupDistinguishesAbsentFromUnavailableProject(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	for _, pid := range []uint64{1, 99999} {
		id, err := a.Access.PutGrant(t.Context(), identity.Spec{SubjectType: "user", Subject: "outsider", Role: access.RoleProjectViewer, Scope: access.ScopeProject, ScopeID: idstest.N(pid).String(), By: "boss"})
		if err != nil {
			t.Fatal(err)
		}
		restore := dbtest.NextStatementFailure(a.DB, "-- name: ProjectByID :one")
		w := authzCall(h, "boss", "DELETE", "/api/v1/authz/grants/"+id.String(), "")
		restore()
		if w.Code != 503 {
			t.Fatalf("unreadable grant project: %d %s", w.Code, w.Body)
		}
		if _, _, found, err := a.Access.LiveGrant(t.Context(), id); err != nil || !found {
			t.Fatalf("unavailable scope revoked grant: %v %v", found, err)
		}
		w = authzCall(h, "boss", "DELETE", "/api/v1/authz/grants/"+id.String(), "")
		if w.Code != 200 {
			t.Fatalf("confirmed existing/absent project cleanup: %d %s", w.Code, w.Body)
		}
	}
}

func TestMCPAuthorityScopeFailureDoesNotOmitOrCreate(t *testing.T) {
	t.Parallel()
	a, boss := mcpWriteFixture(t)
	p, err := a.Projects.BySlug(t.Context(), "app")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		tool string
		skip int
		args map[string]any
	}{
		{"get_project", 0, map[string]any{"slug": "app"}},
		{"create_change", 1, map[string]any{"slug": "app", "title": "must not be created"}},
	} {
		restore := dbtest.StatementFailureAfter(a.DB, "-- name: ProjectByID :one", tc.skip)
		w := mcp_post(a, boss, "", mcp_rpc(7, "tools/call", map[string]any{"name": tc.tool, "arguments": tc.args}))
		restore()
		var response struct {
			Result map[string]any `json:"result"`
			Error  *rpcError      `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Error == nil && response.Result["isError"] != true {
			t.Fatalf("scope failure became successful %s: %d %s", tc.tool, w.Code, w.Body)
		}
	}
	rows, err := a.Work.Changes(asPerson("boss"), p.ID, workflow.ChangeList{})
	if err != nil || len(rows) != 0 {
		t.Fatalf("scope failure created change: %+v %v", rows, err)
	}
}

func TestContributionScopeFailureMintsNoTicket(t *testing.T) {
	t.Parallel()
	a, _ := mcpWriteFixture(t)
	p, err := a.Projects.BySlug(t.Context(), "app")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := a.Work.Create(asPerson("boss"), workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "checkout"})
	if err != nil {
		t.Fatal(err)
	}
	// Work.Change's authority read and the contribution's project read precede
	// the additional current resource lookup in contribution admission.
	restore := dbtest.StatementFailureAfter(a.DB, "-- name: ProjectByID :one", 2)
	got, err := contributionsFor(a).Checkout(asPerson("boss"), ch.ID)
	restore()
	if fault.KindOf(err) != fault.Unavailable || got.Token != "" || got.Ticket != nil {
		t.Fatalf("unreadable scope issued checkout: %+v %v", got, err)
	}
	// sql-guarantee: tickets are readable only by secret, so total absence requires the persisted kind count.
	completionCount(t, a.DB, 0, `SELECT count(*) FROM object_capabilities WHERE kind=$1`, identity.TicketKindGit)
}
