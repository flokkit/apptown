package changeactions

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M) { os.Exit(databaseSuite.Run(m)) }

func TestImmediateAdmissionCallersUseAtomicWorkAndPreserveInputs(t *testing.T) {
	t.Parallel()
	for _, catalog := range []bool{false, true} {
		t.Run(map[bool]string{false: "trigger", true: "catalog"}[catalog], func(t *testing.T) {
			t.Parallel()
			db := databaseSuite.New(t)
			accounts := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
			if err := accounts.SeedAdministrator(context.Background(), "operator"); err != nil {
				t.Fatal(err)
			}
			id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
				return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "admit", Name: "Admit"})
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.InTx(context.Background(), func(tx *database.Tx) error {
				if err := adoption.InitializeIn(t.Context(), tx, id, 0, ""); err != nil {
					return err
				}
				return adoption.RecordIn(t.Context(), tx, id, 1, adoption.AdoptionCreate, "fixture", adoption.EmptyReceipt(), "operator", time.Now())
			}); err != nil {
				t.Fatal(err)
			}
			projects := project.New(db, identity.Scoped(accounts, "project"))
			p, err := projects.BySlug(context.Background(), "admit")
			if err != nil {
				t.Fatal(err)
			}
			work := workflow.New(context.Background(), db, workflow.Dependencies{
				Authority:    access.Checked{Authority: identity.Scoped(accounts, "workflow"), Projects: projects},
				Repositories: &scm.RepositoryLocks{},
				Resolve: func(_ context.Context, req workflow.StartRequest, src workflow.StartSource) (workflow.PreparedRun, error) {
					return workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: req.WorkflowName, Version: 1}, AdoptionID: src.AdoptionID,
						Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}}, nil
				},
			})
			t.Cleanup(work.Stop)
			s := New(work, db)
			c := &wow.CatalogChange{Name: "test/admit", Description: "test", Definition: wow.CatalogDef{Workflow: "test", Title: "Fix {{part}}", Prompt: "Repair {{part}}", Model: "provider/model", Params: []wow.CatalogParam{{Name: "part", Required: true}}}}
			ctx := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{Kind: "user", Name: "operator", Via: "session"}, Receipt: "operator", Verified: true})
			call := func() (*workflow.Change, *workflow.Run, error) {
				if catalog {
					return s.ApplyCatalog(ctx, &p, c, map[string]string{"part": "engine"}, "operator")
				}
				return s.CreateAndStart(ctx, &p, "scheduled", "Repair engine", "brief", "test")
			}
			clear := db.FailNextStatement("INSERT INTO steps")
			ch, run, err := call()
			clear()
			if err == nil || ch != nil || run != nil {
				t.Fatalf("failed admission: %+v %+v %v", ch, run, err)
			}
			changes, err := work.Changes(ctx, id, workflow.ChangeList{})
			if err != nil || len(changes) != 0 {
				t.Fatalf("orphan left behind: %+v %v", changes, err)
			}
			ch, run, err = call()
			if err != nil || ch.RunID != run.ID || run.ChangeID != ch.ID {
				t.Fatalf("success: %+v %+v %v", ch, run, err)
			}
			if catalog {
				var inputs map[string]any
				if err := json.Unmarshal([]byte(run.InputsJSON), &inputs); err != nil {
					t.Fatal(err)
				}
				knobs := wow.CatalogKnobs(run.InputsJSON)
				if ch.Title != "Fix engine" || ch.Body != "Repair engine" || knobs == nil || knobs.Model != "provider/model" || len(inputs) == 0 {
					t.Fatalf("lost catalog input: %+v %+v", ch, inputs)
				}
			}
			// A failed optional notice neither undoes the admission nor escapes
			// as a command failure. The owner still exposes the admitted run.
			clear = db.FailNextStatement("-- name: InsertRunEvent")
			s.RecordConsoleInvocation(ctx, run.ID, "operator", "console/test")
			clear()
			snapshot, err := work.Snapshot(ctx, run.ID, workflow.DetailView)
			if err != nil || snapshot.Run.ChangeID != ch.ID {
				t.Fatalf("notice failure damaged admission: %+v %v", snapshot, err)
			}
			for _, event := range snapshot.Events {
				if event.Kind == "run.invoked" {
					t.Fatal("notice failure was not exercised")
				}
			}
		})
	}
}
