package wow

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/wow/internal/wowsql"
	"flokkit.io/forge/internal/ids"

	"github.com/jackc/pgx/v5/pgconn"

	"flokkit.io/forge/internal/database"
)

func TestWorkflowCatalogObservationDistinguishesFailureFromNoEntries(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := New(db, Dependencies{})
	if rows, err := s.Workflows(t.Context()); err != nil || len(rows) != 0 {
		t.Fatalf("empty catalog: %+v %v", rows, err)
	}
	if _, _, err := s.workflows.Put(t.Context(), sprintf(laneYAML, "readable", "valid", ""), "alice"); err != nil {
		t.Fatal(err)
	}
	// Preserve the catalog's existing treatment of individually incomplete or
	// malformed entries. A statement failure is a different condition.
	q := wowsql.New(db.SQL())
	if _, err := q.InsertWorkflow(context.Background(), wowsql.InsertWorkflowParams{ID: ids.New(time.Now().UTC()), Name: "missing-publication", At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	bad, err := q.InsertWorkflow(context.Background(), wowsql.InsertWorkflowParams{ID: ids.New(time.Now().UTC()), Name: "malformed-publication", At: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.InsertWorkflowVersion(context.Background(), wowsql.InsertWorkflowVersionParams{ID: ids.New(time.Now()), LibID: bad.ID, Version: 1, Yaml: "[", At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.Workflows(t.Context()); err != nil || len(rows) != 1 || rows[0].Name != "readable" {
		t.Fatalf("individual unusable entries changed catalog behavior: %+v %v", rows, err)
	}
	dbtest.FailNextStatement(t, db, "name: WorkflowCatalog")
	if rows, err := s.Workflows(t.Context()); rows != nil || !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("failed catalog became an empty or partial observation: %+v %v", rows, err)
	}
}

func TestWorkflowObservationOnlyFallsBackForAbsentSystemBinding(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := New(db, Dependencies{})
	if err := s.workflows.StockInsert(t.Context(), "land", "system", sprintf(laneYAML, "land", "system", "  system: true\n")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.workflows.Put(t.Context(), sprintf(laneYAML, "custom-land", "system", "  system: true\n"), "alice"); err != nil {
		t.Fatal(err)
	}
	ref := WorkflowRef{Name: "land", Version: 1, ResolveSystem: true}
	if got, err := s.Workflow(t.Context(), ref); err != nil || got.Name != "land" || got.Version != 1 {
		t.Fatalf("missing binding did not retain the stock name: %+v %v", got, err)
	}
	if err := s.workflows.BindSystem(t.Context(), "land", "custom-land", "alice"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Workflow(t.Context(), ref); err != nil || got.Name != "custom-land" || got.Version != 1 {
		t.Fatalf("binding was not resolved: %+v %v", got, err)
	}
	dbtest.FailNextStatement(t, db, "name: SystemWorkflow")
	var failure *pgconn.PgError
	if got, err := s.Workflow(t.Context(), ref); !errors.As(err, &failure) || failure.Code != "42883" || got.YAML != "" {
		t.Fatalf("failed binding read silently selected another workflow: %+v %v", got, err)
	}
	ref.Version = 99
	if _, err := s.Workflow(t.Context(), ref); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing exact publication silently resolved latest: %v", err)
	}
}
