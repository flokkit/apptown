package httpapi

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// workflowHTTPFixture constructs the actual Work owner and its identity facts
// for direct handler tests. It has no App, route mux, or exported test API.
type workflowHTTPFixture struct {
	DB                         *database.DB
	Access                     *identity.Service
	Projects                   *project.Projects
	Work                       *workflow.Service
	ProjectID, ChangeID, RunID ids.ID
	Context                    context.Context
}

// HTTP tests observe the handler and Work transaction; the separate AppTown
// integration tests cover the River/Identity cleanup participant.
type workflowHTTPAbandonRecorder struct{}

func (workflowHTTPAbandonRecorder) RecordAbandonIn(context.Context, *database.Tx, workflow.AbandonEffects) error {
	return nil
}

func newWorkflowHTTPFixture(t *testing.T, withRun bool) workflowHTTPFixture {
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"approvers": []string{"bls"}}}}}}}
	return newWorkflowHTTPFixtureWithSpec(t, withRun, spec, nil, nil)
}

func newWorkflowHTTPFixtureWithSpec(t *testing.T, withRun bool, spec schema.Spec, automatic []string, runners map[string]workflow.Runner) workflowHTTPFixture {
	t.Helper()
	return newWorkflowHTTPFixtureWithDefinitions(t, withRun, map[string]schema.Spec{"": spec}, automatic, runners)
}

func newWorkflowHTTPFixtureWithDefinitions(t *testing.T, withRun bool, definitions map[string]schema.Spec, automatic []string, runners map[string]workflow.Runner) workflowHTTPFixture {
	t.Helper()
	db := databaseSuite.New(t)
	directory := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := directory.SeedAdministrator(context.Background(), "bls"); err != nil {
		t.Fatal(err)
	}
	projects := project.New(db, identity.Scoped(directory, "project"))
	projectID, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "shop", Name: "Shop"})
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "bls", Via: "session"}, Verified: true})
	work := workflow.New(context.Background(), db, workflow.Dependencies{
		Authority:    access.Checked{Authority: identity.Scoped(directory, "workflow"), Projects: projects},
		Abandonment:  workflowHTTPAbandonRecorder{},
		Repositories: &scm.RepositoryLocks{},
		Automatic:    automatic,
		Runners:      runners,
		Resolve: func(_ context.Context, request workflow.StartRequest, source workflow.StartSource) (workflow.PreparedRun, error) {
			spec, ok := definitions[request.WorkflowName]
			if !ok {
				spec = definitions[""]
			}
			return workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: request.WorkflowName, Version: 1}, Spec: spec, AdoptionID: source.AdoptionID}, nil
		},
	})
	t.Cleanup(work.Stop)
	change, err := work.Create(ctx, workflow.ChangeDraft{ProjectID: projectID, Title: "original"})
	if err != nil {
		t.Fatal(err)
	}
	fixture := workflowHTTPFixture{DB: db, Access: directory, Projects: projects, Work: work, ProjectID: projectID, ChangeID: change.ID, Context: ctx}
	if withRun {
		if err := db.InTx(ctx, func(tx *database.Tx) error {
			if err := adoption.InitializeIn(ctx, tx, projectID, 0, "", ""); err != nil {
				return err
			}
			return adoption.RecordIn(ctx, tx, projectID, 1, adoption.AdoptionCreate, "HTTP fixture", adoption.EmptyReceipt(), "fixture", time.Now())
		}); err != nil {
			t.Fatal(err)
		}
		run, err := work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "test"})
		if err != nil {
			t.Fatal(err)
		}
		fixture.RunID = run.ID
	}
	return fixture
}

func newWorkflowHTTPAuthorityFixture(t *testing.T) workflowHTTPFixture {
	t.Helper()
	f := newWorkflowHTTPFixture(t, true)
	grantWorkflowHTTPAuthority(t, f)
	return f
}

func newWorkflowHTTPAuthorityFixtureWithSpec(t *testing.T, spec schema.Spec) workflowHTTPFixture {
	t.Helper()
	f := newWorkflowHTTPFixtureWithSpec(t, true, spec, nil, nil)
	grantWorkflowHTTPAuthority(t, f)
	return f
}

func grantWorkflowHTTPAuthority(t *testing.T, f workflowHTTPFixture) {
	t.Helper()
	otherProjectID, err := database.Transact(context.Background(), f.DB, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(f.Context, tx, time.Now(), project.Spec{Slug: "elsewhere", Name: "Elsewhere"})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"owner", "outsider"} {
		if _, err := f.Access.EnsureUser(context.Background(), user); err != nil {
			t.Fatal(err)
		}
	}
	for _, grant := range []struct {
		user      string
		projectID ids.ID
	}{{"owner", f.ProjectID}, {"outsider", otherProjectID}} {
		for _, role := range []string{access.RoleProjectApprover, access.RoleProjectMaintainer} {
			if _, err := f.Access.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: grant.user, Role: role, Scope: access.ScopeProject, ScopeID: fmt.Sprint(grant.projectID), Origin: "fixture", By: "bls"}, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
}
