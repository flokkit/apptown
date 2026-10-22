package usage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/usage/internal/usagesql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestSummaryKeepsScopeWindowAndRollupDimensions(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	l := New(db)
	var made []ids.ID
	for _, slug := range []string{"first", "second"} {
		id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
			return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: slug})
		})
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, id)
	}
	now := time.Now().UTC()
	yesterday := now.AddDate(0, 0, -1)
	// Report fixtures deliberately include an orphan project, a different workflow,
	// a console, and usage outside the window. UTC day grouping must agree across
	// the day and stacked-kind reports.
	for _, row := range []struct {
		project        ids.ID
		workflow, kind string
		total          int64
		at             time.Time
	}{
		{made[0], "build", "step", 10, yesterday}, {made[0], "", "console", 20, now},
		{made[1], "build", "step", 30, now}, {made[0], "other", "step", 40, now},
		{idstest.N(999), "build", "step", 50, now}, {made[0], "build", "step", 1000, now.AddDate(0, 0, -40)},
	} {
		if err := usagesql.New(db.SQL()).RecordUsage(context.Background(), usagesql.RecordUsageParams{ID: ids.New(row.at), At: row.at, Kind: row.kind, ProjectID: row.project, WorkflowName: row.workflow, Provider: "provider", Model: "model", Input: row.total, Output: row.total * 2, Total: row.total, CostMicros: row.total * 10}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := l.Summary(context.Background(), 30, ids.Nil, "")
	if err != nil || all.Totals.Total != 150 || all.Totals.CostMicros != 1500 || len(all.ByProject) != 2 || all.ByProject[0].Key != "first" || all.ByProject[0].Total != 70 || len(all.ByModel) != 1 || all.ByModel[0].Key != "provider/model" {
		t.Fatalf("instance: %+v %v", all, err)
	}
	if len(all.ByDay) != 2 || all.ByDay[0].Key != yesterday.Format("2006-01-02") || all.ByDay[0].Total != 10 || all.ByDay[1].Total != 140 || len(all.ByDayKind) != 3 {
		t.Fatalf("daily: %+v", all)
	}
	scoped, err := l.Summary(context.Background(), 30, made[0], "")
	if err != nil || scoped.Totals.Total != 70 || len(scoped.ByWorkflow) != 3 || scoped.ByWorkflow[1].Key != "(interactive console)" {
		t.Fatalf("project: %+v %v", scoped, err)
	}
	filtered, err := l.Summary(context.Background(), 30, made[0], "build")
	if err != nil || filtered.Totals.Total != 10 || len(filtered.ByWorkflow) != 1 || filtered.ByWorkflow[0].Total != 10 {
		t.Fatalf("project and workflow: %+v %v", filtered, err)
	}
	across, err := l.Summary(context.Background(), 30, ids.Nil, "build")
	if err != nil || across.Totals.Total != 90 || len(across.ByProject) != 2 {
		t.Fatalf("workflow across projects: %+v %v", across, err)
	}
	empty, err := l.Summary(context.Background(), 30, made[0], "absent")
	if err != nil || empty.Totals.Total != 0 || len(empty.ByKind) != 0 || empty.ByKind == nil || empty.ByDayKind == nil {
		t.Fatalf("empty: %+v %v", empty, err)
	}
}

func TestSummaryAppliesTopLimitBeforeRemovingEmptyKeys(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	for i := 0; i < 51; i++ {
		kind := fmt.Sprintf("kind-%02d", i)
		if i == 0 {
			kind = ""
		}
		if err := New(db).Record(context.Background(), Entry{Kind: kind}, Totals{Total: int64(100 - i)}, ""); err != nil {
			t.Fatal(err)
		}
	}
	r, err := New(db).Summary(context.Background(), 30, ids.Nil, "")
	if err != nil || len(r.ByKind) != 49 || r.ByKind[0].Key != "kind-01" || r.ByKind[48].Key != "kind-49" {
		t.Fatalf("top kinds: %+v %v", r.ByKind, err)
	}
}
