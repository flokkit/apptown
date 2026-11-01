package apptown

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
)

func TestMissingRunAdoptionNeverReadsCurrentProject(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	a.SyncStock(t.Context())
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "p", Name: "P", Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	run := &workflow.Run{ID: idstest.N(99), ProjectID: p.ID}
	receipt, _ := adoption.ProjectAdoptionIn(context.Background(), a.DB, run.ProjectID, run.AdoptionID)
	if receipt != nil {
		t.Fatal("missing run receipt selected current project")
	}
	if _, err := a.Adoption.Resolve(t.Context(), adoption.Source{ProjectID: run.ProjectID, Revision: receipt}); err == nil {
		t.Fatal("missing run receipt compiled current guidance")
	}
	if _, err := a.Adoption.Checks(t.Context(), adoption.Source{ProjectID: run.ProjectID, Revision: receipt}); err == nil {
		t.Fatal("missing run receipt selected current checks")
	}
	if _, _, err := a.Adoption.ResolveBlock(t.Context(), adoption.Source{ProjectID: run.ProjectID, Revision: receipt}, "skill/implementation"); err == nil {
		t.Fatal("missing run receipt selected current skill")
	}
}

func TestMissingPinnedWorkflowNeverSelectsNewerVersion(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	a.SyncStock(t.Context())
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "p", Name: "P", Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	ad := currentAdoption(a.DB, p.ID)
	workflows, err := a.Ways.Workflows(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var latest int
	var description string
	for _, entry := range workflows {
		if entry.Name == "land" {
			latest, description = entry.Version, entry.Doc.Metadata.Description
			break
		}
	}
	if latest == 0 {
		t.Fatal("land workflow was not listed")
	}
	_, yaml, err := a.Ways.WorkflowVersion(t.Context(), "land", latest)
	if err != nil {
		t.Fatal(err)
	}
	if version, err := wow.NewWorkflows(a.DB).StockUpdate(t.Context(), "land", description, yaml+"\n# newer"); err != nil || version != 2 {
		t.Fatalf("publish newer land: version=%d err=%v", version, err)
	}
	// sql-guarantee: Remove only the version named by the frozen receipt while a newer version remains; catalog operations preserve immutable publications and cannot simulate a missing pin.
	completionExec(t, a.DB, `DELETE FROM lib_workflow_versions WHERE lib_id=(SELECT id FROM lib_workflows WHERE name='land') AND version=1`)
	if _, _, err := a.Adoption.WorkflowVersion(t.Context(), adoption.Source{ProjectID: ad.ProjectID, Revision: ad}, "land"); err == nil {
		t.Fatal("missing pin selected newer workflow")
	}
	if _, err := a.recordAdoption(p, "edit", "unrelated edit", "fixture"); err == nil {
		t.Fatal("edit silently replaced missing workflow pin")
	}
	if currentAdoption(a.DB, p.ID).ID != ad.ID {
		t.Fatal("refused edit changed receipt")
	}
	// sql-guarantee: Point current adoption at a nonexistent revision; RecordIn atomically creates its receipt and cannot create this contradiction.
	completionExec(t, a.DB, `UPDATE adoptions SET revision=999 WHERE project_id=$1`, p.ID)
	if _, err := a.recordAdoption(p, "edit", "missing receipt", "fixture"); err == nil {
		t.Fatal("missing current receipt reconstructed")
	}
}
