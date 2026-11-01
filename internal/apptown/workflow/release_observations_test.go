package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestReleaseChangeBatchChecksBothProjectsAndKeepsEmptyLists(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	ctx := context.Background()
	other := workflowTestProject(t, q.db, "private", "Private", "")
	foreign := admissionTestChange(t, q.db, other, "secret")
	// sql-guarantee: the Workflow private batch reader needs released ledgers with exact per-project numbers and no release-operation side effects; Deployment Start cannot create this immutable released snapshot.
	release := completionInsert(t, q.db, `INSERT INTO releases(id,project_id,number,state,deployment,created_at) VALUES ($1,$2,1,'released','main','2026-10-01')`, run.ProjectID)
	// sql-guarantee: the Workflow private batch reader needs released ledgers with exact per-project numbers and no release-operation side effects; Deployment Start cannot create this immutable released snapshot.
	empty := completionInsert(t, q.db, `INSERT INTO releases(id,project_id,number,state,deployment,created_at) VALUES ($1,$2,2,'released','main','2026-10-01')`, run.ProjectID)
	// sql-guarantee: the Workflow private batch reader needs released ledgers with exact per-project numbers and no release-operation side effects; Deployment Start cannot create this immutable released snapshot.
	foreignRelease := completionInsert(t, q.db, `INSERT INTO releases(id,project_id,number,state,deployment,created_at) VALUES ($1,$2,1,'released','main','2026-10-01')`, other)
	// sql-guarantee: the project fence requires deliberately cross-project release membership that Deployment operations reject, so only the private read representation can create it.
	completionExec(t, q.db, `INSERT INTO release_changes(release_id,change_id) VALUES ($1,$2),($3,$4),($5,$6)`, release, run.ChangeID, release, foreign, foreignRelease, foreign)
	counted := &countedQueries{Handle: q.db.SQL()}
	contents, err := readReleaseChanges(ctx, counted, run.ProjectID, []ids.ID{release, empty, foreignRelease, idstest.N(99999)})
	if err != nil || counted.reads != 1 || len(contents[release]) != 1 || contents[release][0].ID != run.ChangeID || contents[release][0].Number != storedChange(t, q.db, run.ChangeID).Number {
		t.Fatalf("contents %+v reads %d: %v", contents, counted.reads, err)
	}
	for _, id := range []ids.ID{empty, foreignRelease, idstest.N(99999)} {
		if contents[id] == nil || len(contents[id]) != 0 {
			t.Fatalf("nonempty/missing array %d: %+v", id, contents[id])
		}
	}
	s := newService(q.db, allowProject(run.ProjectID), Runtime{})
	if _, err := s.ReleaseChanges(ctx, other, []ids.ID{foreignRelease}); err == nil {
		t.Fatal("foreign release project admitted")
	}
	transitionStoredChangeForTest(t, q.db, run.ChangeID, ChangeAbandoned, true)
	contents, err = s.ReleaseChanges(ctx, run.ProjectID, []ids.ID{release})
	if err != nil || len(contents[release]) != 1 {
		t.Fatalf("historic membership lost %+v: %v", contents, err)
	}
	dbtest.FailNextStatement(t, q.db, "SELECT rel.id AS release_id")
	if _, err := s.ReleaseChanges(ctx, run.ProjectID, []ids.ID{release}); !errors.Is(err, dbtest.ErrInjectedFailure) {
		t.Fatalf("hidden release failure: %v", err)
	}
}

func TestShippedSinceAndSBOMCutoffs(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	ctx := context.Background()
	cutoff := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	// sql-guarantee: the cutoff law requires exact closed_at boundary timestamps or an abandoned comparator; TransitionChange always stamps the current database time.
	completionExec(t, q.db, `UPDATE changes SET state='shipped',closed_at=$1 WHERE id=$2`, cutoff, run.ChangeID)
	// sql-guarantee: the cutoff law requires exact closed_at boundary timestamps or an abandoned comparator; TransitionChange always stamps the current database time.
	newer := completionInsert(t, q.db, `INSERT INTO changes(id,number,project_id,type,title,state,closed_at,created_at) VALUES ($1,$2,$3,'feature','new','shipped',$4,'2026-10-09T00:00:00Z')`, fixtureNumber(), run.ProjectID, cutoff.Add(time.Second))
	insertChangeForTest(t, q.db, workflowsql.InsertChangeParams{ID: ids.New(time.Now()), CreatedAt: time.Now(), ProjectID: run.ProjectID, Type: "feature", Title: "undated", State: ChangeShipped.String()})
	// sql-guarantee: the cutoff law requires exact closed_at boundary timestamps or an abandoned comparator; TransitionChange always stamps the current database time.
	completionInsert(t, q.db, `INSERT INTO changes(id,number,project_id,type,title,state,closed_at,created_at) VALUES ($1,$2,$3,'feature','abandoned','abandoned',$4,'2026-10-09T00:00:00Z')`, fixtureNumber(), run.ProjectID, cutoff.Add(time.Hour))
	s := newService(q.db, allowProject(run.ProjectID), Runtime{})
	changes, err := s.ShippedChangesSince(ctx, run.ProjectID, cutoff, 30)
	if err != nil || len(changes) != 1 || changes[0].ID != newer || changes[0].Number != storedChange(t, q.db, newer).Number {
		t.Fatalf("shipped %+v: %v", changes, err)
	}
	changes, err = s.ShippedChangesSince(ctx, run.ProjectID, time.Time{}, 0)
	if err != nil || len(changes) != 2 {
		t.Fatalf("all dated history %+v: %v", changes, err)
	}
	artifact := insertTextArtifactForTest(t, q.db, run.ID, "sbom", "at-cutoff", "", "body", cutoff)
	insertTextArtifactForTest(t, q.db, run.ID, "sbom", "after", "", "body", cutoff.Add(time.Second))
	id, err := LatestSBOMBeforeIn(ctx, q.db, run.ProjectID, cutoff)
	if err != nil || id != artifact {
		t.Fatalf("SBOM %d: %v", id, err)
	}
	id, err = LatestSBOMBeforeIn(ctx, q.db, idstest.N(999999), cutoff)
	if err != nil || !id.IsZero() {
		t.Fatalf("foreign SBOM %d: %v", id, err)
	}
	if _, err := s.ShippedChangesSince(ctx, run.ProjectID, cutoff, -1); err == nil {
		t.Fatal("negative limit admitted")
	}
}
