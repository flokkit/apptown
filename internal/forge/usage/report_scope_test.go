package usage

import (
	"context"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/usage/internal/usagesql"
	"flokkit.io/forge/internal/ids"
)

// projectReader reads the instance when instance is set, otherwise only the
// listed projects; failing stands for policy that could not be read.
type projectReader struct {
	instance bool
	projects []ids.ID
	failing  error
}

func (r projectReader) Require(_ context.Context, p access.Permission) (access.Caller, error) {
	if r.failing != nil {
		return access.Caller{}, r.failing
	}
	if r.instance {
		return access.Caller{}, nil
	}
	for _, id := range r.projects {
		if p.Resource.ProjectID == id {
			return access.Caller{}, nil
		}
	}
	return access.Caller{}, &access.Denied{Code: "fixture", Reason: "not yours"}
}
func (r projectReader) VisibleProjects(context.Context, access.Capability) ([]ids.ID, error) {
	return r.projects, nil
}

// A report covers only what its caller may read: a project reader's totals
// and breakdowns leave other projects and unattributed usage out, a named
// project they cannot read is refused, and a policy failure is an error,
// never a narrower report.
func TestReportCoversOnlyWhatTheCallerMayRead(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	var made []ids.ID
	for _, slug := range []string{"theirs", "mine"} {
		id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
			return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: slug})
		})
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, id)
	}
	for _, row := range []struct {
		project ids.ID
		total   int64
	}{{made[0], 100}, {made[1], 7}, {ids.Nil, 1000}} {
		if err := usagesql.New(db.SQL()).RecordUsage(context.Background(), usagesql.RecordUsageParams{ID: ids.New(time.Now().UTC()), At: time.Now().UTC(), Kind: "step", ProjectID: row.project, WorkflowName: "build", Provider: "p", Model: "m", Total: row.total, CostMicros: row.total}); err != nil {
			t.Fatal(err)
		}
	}
	ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "reader"}, Verified: true})
	member := NewService(db, projectReader{projects: []ids.ID{made[1]}}, nil)
	rep, err := member.Report(ctx, Query{WindowDays: 30})
	if err != nil || rep.Totals.Total != 7 || len(rep.ByProject) != 1 || rep.ByProject[0].Key != "mine" || len(rep.ByModel) != 1 || rep.ByModel[0].Total != 7 {
		t.Fatalf("project reader: %+v %v", rep, err)
	}
	if own, err := member.Report(ctx, Query{WindowDays: 30, ProjectID: made[1]}); err != nil || own.Totals.Total != 7 || len(own.ByWorkflow) != 1 {
		t.Fatalf("own project: %+v %v", own, err)
	}
	var denied *access.Denied
	if other, err := member.Report(ctx, Query{WindowDays: 30, ProjectID: made[0]}); !errors.As(err, &denied) || other.Totals.Total != 0 {
		t.Fatalf("another project: %+v %v", other, err)
	}
	if none, err := NewService(db, projectReader{}, nil).Report(ctx, Query{WindowDays: 30}); err != nil || none.Totals.Total != 0 || len(none.ByDay) != 0 {
		t.Fatalf("no projects read nothing: %+v %v", none, err)
	}
	if all, err := NewService(db, projectReader{instance: true}, nil).Report(ctx, Query{WindowDays: 30}); err != nil || all.Totals.Total != 1107 || len(all.ByProject) != 2 {
		t.Fatalf("instance reader: %+v %v", all, err)
	}
	// A run bound to project 2 whose authority would read the instance still
	// sees only what visibility gives it.
	run := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "instrument", Name: "run/1", Instrument: "run", ProjectID: made[1]}, Verified: true})
	if bound, err := NewService(db, projectReader{instance: true, projects: []ids.ID{made[1]}}, nil).Report(run, Query{WindowDays: 30}); err != nil || bound.Totals.Total != 7 || len(bound.ByProject) != 1 {
		t.Fatalf("bound run: %+v %v", bound, err)
	}
	broken := errors.New("grants unreadable")
	if _, err := NewService(db, projectReader{failing: broken}, nil).Report(ctx, Query{WindowDays: 30}); !errors.Is(err, broken) {
		t.Fatalf("policy failure: %v", err)
	}
	if _, err := NewService(db, nil, nil).Report(ctx, Query{WindowDays: 30}); !errors.Is(err, access.ErrAuthorityUnavailable) {
		t.Fatalf("no authority: %v", err)
	}
}
