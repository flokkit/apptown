package identity

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// An instrument gets only the capabilities and project scope its identity
// allows, even when its person has a broader role elsewhere.
func TestAuthzInstrumentsAreBounded(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	service := New(db, Config{})
	if err := service.SeedAdministrator(context.Background(), "boss"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"owner", "outsider"} {
		if _, err := service.EnsureUser(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []project.Spec{{Slug: "game", Name: "G"}, {Slug: "open", Name: "O"}} {
		if _, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) { return project.CreateAt(t.Context(), tx, time.Now(), p) }); err != nil {
			t.Fatal(err)
		}
	}
	for _, spec := range []Spec{
		{SubjectType: "user", Subject: "owner", Role: access.RoleProjectMaintainer, Scope: access.ScopeProject, ScopeID: idstest.N(1).String(), By: "boss"},
		{SubjectType: "user", Subject: "owner", Role: access.RoleProjectApprover, Scope: access.ScopeProject, ScopeID: idstest.N(1).String(), By: "boss"},
		{SubjectType: "group", Subject: GroupAllUsers, Role: access.RoleProjectMaintainer, Scope: access.ScopeProject, ScopeID: idstest.N(2).String(), By: "boss"},
	} {
		if _, err := service.PutGrant(context.Background(), spec); err != nil {
			t.Fatal(err)
		}
	}
	resource := func(id ids.ID, name string) access.Resource {
		return access.Resource{Type: "project", ID: id.String(), ProjectID: id, ProjectName: name}
	}
	holds := func(pr access.Principal, cap access.Capability, res access.Resource) bool {
		t.Helper()
		dec, err := service.Explain(context.Background(), pr, access.Permission{Capability: cap, Resource: res})
		if err != nil {
			t.Fatal(err)
		}
		return dec.Allow
	}
	run := access.Principal{Kind: "instrument", Name: "run/1", Instrument: "run", ProjectID: idstest.N(1)}
	if holds(run, access.CapChangeApprove, resource(idstest.N(1), "G")) {
		t.Error("a run token may not approve")
	}
	if !holds(run, access.CapChangeRunChecks, resource(idstest.N(1), "G")) {
		t.Error("a run token may run checks in its project")
	}
	if holds(run, access.CapChangeRunChecks, resource(idstest.N(2), "O")) {
		t.Error("a run token may not act on another project")
	}
	console := access.Principal{Kind: "instrument", Name: "owner via console/abc", Person: "owner", Instrument: "console", ProjectID: idstest.N(1)}
	if !holds(console, access.CapChangeRequestLand, resource(idstest.N(1), "G")) {
		t.Error("a console may hand a change off for landing when its person may")
	}
	if holds(console, access.CapChangeLand, resource(idstest.N(1), "G")) {
		t.Error("a console never operates a landing attempt")
	}
	outsiderConsole := access.Principal{Kind: "instrument", Name: "outsider via console/x", Person: "outsider", Instrument: "console", ProjectID: idstest.N(1)}
	if holds(outsiderConsole, access.CapChangeRequestLand, resource(idstest.N(1), "G")) {
		t.Error("a console carries no more than its person holds")
	}
}
