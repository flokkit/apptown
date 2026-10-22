package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// Count real database round trips at the same interface generated code uses.
type countedQueries struct {
	database.Handle
	reads  int
	writes int
}

func (d *countedQueries) SQL() database.Handle {
	return d.Handle.WithExecutor(d)
}
func (d *countedQueries) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	d.writes++
	return d.DBTX.ExecContext(ctx, q, args...)
}
func (d *countedQueries) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	d.reads++
	return d.DBTX.QueryContext(ctx, q, args...)
}
func (d *countedQueries) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	d.reads++
	return d.DBTX.QueryRowContext(ctx, q, args...)
}

func TestLibraryCatalogReadsDoNotGrowWithWorkflowCount(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	projects := make([]ids.ID, 0, 2)
	for _, slug := range []string{"selected", "other"} {
		id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
			return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: slug})
		})
		if err != nil {
			t.Fatal(err)
		}
		projects = append(projects, id)
	}
	declaration := func(name, description string) string {
		return fmt.Sprintf("apiVersion: apptown.dev/v1\nkind: Workflow\nmetadata: {name: %s, description: %s}\nspec: {kind: pipeline, stages: [{name: work, steps: [{name: work, uses: script, with: {run: 'true'}}]}]}\n", name, description)
	}
	library := wow.NewWorkflows(db)
	selection := adoption.New(db, adoption.Dependencies{Ways: wow.New(db, wow.Dependencies{})})
	for i := range 12 {
		name := fmt.Sprintf("wf-%02d", i)
		for _, description := range []string{"original", "current"} {
			if _, _, err := library.Put(t.Context(), declaration(name, description), "alice"); err != nil {
				t.Fatal(err)
			}
		}
		for i, pid := range projects {
			if err := selection.SelectLane(t.Context(), pid, name, i == 0, "alice"); err != nil {
				t.Fatal(err)
			}
		}
	}
	counted := &countedQueries{Handle: db.SQL()}
	catalog, err := loadLibraryCatalog(context.Background(), counted)
	if err != nil || counted.reads != 3 || len(catalog.workflows) != 12 || len(catalog.projects) != 2 {
		t.Fatalf("catalog: %d reads, %d workflows: %v", counted.reads, len(catalog.workflows), err)
	}
	for _, wf := range catalog.workflows {
		if wf.Yaml != declaration(wf.Name, "current") || strings.Join(catalog.selected[wf.Name], ",") != "selected" {
			t.Fatalf("catalog mixed versions or disabled lanes: %+v", wf)
		}
	}
	for _, version := range []int{0, 1, 2} {
		counted.reads = 0
		gotVersion, yaml, err := wow.WorkflowVersionIn(t.Context(), counted, "wf-00", version)
		wantVersion, wantYAML := 2, declaration("wf-00", "current")
		if version == 1 {
			wantVersion, wantYAML = 1, declaration("wf-00", "original")
		}
		if err != nil || counted.reads != 1 || gotVersion != wantVersion || yaml != wantYAML {
			t.Fatalf("version %d: got %d %q in %d reads: %v", version, gotVersion, yaml, counted.reads, err)
		}
	}
	if _, _, err := wow.WorkflowVersionIn(t.Context(), db, "wf-00", 99); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing historical version silently fell back: %v", err)
	}
}
