package workflow

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func TestCallerAttribution(t *testing.T) {
	for _, c := range []access.Caller{
		{Principal: access.Principal{Kind: "user", Name: "owner"}, Verified: true, Receipt: "owner via client"},
		{Principal: access.Principal{Kind: "instrument", Name: "owner via console/c", Person: "owner", Instrument: "console"}, Verified: true},
	} {
		got := callerActor(c)
		if got.InferenceOwner != "owner" || got.Receipt == "" {
			t.Fatalf("typed attribution: %+v", got)
		}
	}
}
func TestGroupSatisfiesApprover(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	directory := identity.New(db, identity.Config{})
	if err := directory.SeedAdministrator(context.Background(), "boss"); err != nil {
		t.Fatal(err)
	}
	principal := testAdmittedPrincipal(t, directory, "boss")
	if err := directory.SaveGroup(context.Background(), identity.GroupChange{Name: "parents", Members: []string{"bls"}}, principal); err != nil {
		t.Fatal(err)
	}

	projectID := workflowTestProject(t, db, "approvers", "Approvers", "")
	scope := operationPermission{ProjectID: projectID}
	approvers := []string{"grandma", "group:parents"}
	if err := requireApprover(context.Background(), db, scope, approvers, "grandma", ids.Nil, time.Now()); err != nil {
		t.Fatal("named approver rejected")
	}
	if err := requireApprover(context.Background(), db, scope, approvers, "bls", ids.Nil, time.Now()); err != nil {
		t.Fatal("group member rejected")
	}
	if err := requireApprover(context.Background(), db, scope, approvers, "kid", ids.Nil, time.Now()); err == nil {
		t.Fatal("non-member accepted")
	}
	if err := requireApprover(context.Background(), db, scope, approvers, "parents", ids.Nil, time.Now()); err == nil {
		t.Fatal("the group NAME is not a user")
	}
}

func TestRunScopeUsesCurrentRecord(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	p := workflowTestProject(t, db, "scope", "Scope", "")
	id := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: p, WorkflowName: "test", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "work", DefinitionJson: []byte(`{}`)}).ID
	scope := (&Service{db: db}).RunScope(id)
	if got, err := scope.ProjectID(context.Background()); err != nil || got != p {
		t.Fatalf("recorded scope: %d %v", got, err)
	}
	dbtest.FailNextStatement(t, db, "SELECT project_id FROM runs")
	if _, err := scope.ProjectID(context.Background()); err == nil {
		t.Fatal("unavailable binding reused prior project")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scope.ProjectID(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := workflowsql.New(db.SQL()).DeleteProjectRuns(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := scope.ProjectID(context.Background()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}
