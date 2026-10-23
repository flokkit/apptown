package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type authorityFunc func(context.Context, operationPermission) (string, error)

func (f authorityFunc) Require(ctx context.Context, p operationPermission) (commandActor, error) {
	who, err := f(ctx, p)
	return commandActor{Receipt: who, InferenceOwner: who}, err
}
func allowProject(project ids.ID) operationAuthority {
	return projectAuthority{project: project, authorityFunc: authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
		if p.Action != actionIdentify && p.ProjectID != project {
			return "", &access.Denied{Reason: "outside caller scope"}
		}
		return "verified-person", nil
	})}
}

func TestWorkServiceRequiresAuthorityBeforeAccess(t *testing.T) {
	t.Parallel()
	for _, auth := range []operationAuthority{nil, authorityFunc(func(context.Context, operationPermission) (string, error) { return "", nil })} {
		s := newService(nil, auth, Runtime{})
		reads := []func() error{
			func() error { return s.RefreshAssertions(context.Background(), idstest.N(1)) },
			func() error { _, e := s.ChangeHistory(context.Background(), HistoryQuery{}); return e },
			func() error { _, e := s.OpenChangesWithoutRuns(context.Background(), idstest.N(1), 10); return e },
			func() error { _, e := s.RunningSteps(context.Background()); return e },
			func() error { _, e := s.ExecutingSteps(context.Background()); return e },
			func() error { _, e := s.Change(context.Background(), idstest.N(1)); return e },
			func() error { _, e := s.Run(context.Background(), idstest.N(1)); return e }, func() error { _, e := s.Changes(context.Background(), idstest.N(1), ChangeList{}); return e },
			func() error { _, e := s.Snapshot(context.Background(), idstest.N(1), DetailView); return e }, func() error { _, e := s.Artifact(context.Background(), idstest.N(1)); return e },
			func() error { _, e := s.Runs(context.Background(), RunQuery{}); return e }, func() error { _, e := s.Activities(context.Background(), []ids.ID{idstest.N(1)}); return e },
			func() error {
				_, e := s.Create(context.Background(), ChangeDraft{ProjectID: idstest.N(1), Title: "cannot enter"})
				return e
			},
			func() error { _, e := s.Edit(context.Background(), ChangeEdit{ID: idstest.N(1)}); return e },
			func() error {
				_, e := s.Import(context.Background(), ImportBranch{ProjectID: idstest.N(1), Branch: "main"})
				return e
			}, func() error { _, e := s.Uplift(context.Background(), Uplift{ProjectID: idstest.N(1)}); return e },
		}
		for i, read := range reads {
			if err := read(); !errors.Is(err, ErrCallerRequired) {
				t.Fatalf("operation %d: %v", i, err)
			}
		}
	}
}

func TestWorkServiceFiltersBeforeLimitsAndRefusesMixedBatch(t *testing.T) {
	t.Parallel()
	q, root, _ := activityFixture(t)
	ctx := context.Background()
	other := workflowTestProject(t, q.db, "foreign", "Foreign", "")
	stored, err := workflowsql.New(q.db.SQL()).InsertRun(ctx, workflowsql.InsertRunParams{ID: ids.New(time.Now().UTC()), ProjectID: other, WorkflowName: "secret", WorkflowVersion: 1, Kind: "pipeline", Status: "invalid secret state", CurrentStage: "finish", DefinitionJson: json.RawMessage(`{}`), InputsJson: json.RawMessage(`{}`), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC().Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	foreign := stored.ID
	s := newService(q.db, allowProject(root.ProjectID), Runtime{})
	rows, err := s.Runs(ctx, RunQuery{Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].ID != root.ID {
		t.Fatalf("visibility before limit/decoding: %+v %v", rows, err)
	}
	if rows, err := s.Runs(ctx, RunQuery{ProjectSlug: "foreign"}); rows != nil || err == nil {
		t.Fatalf("explicit foreign list: %+v %v", rows, err)
	}
	var denied *access.Denied
	if out, err := s.Activities(ctx, []ids.ID{root.ID, foreign}); out != nil || !errors.As(err, &denied) {
		t.Fatalf("foreign batch must fail at authority before decoding: %+v %v", out, err)
	}
	if out, err := s.Snapshot(ctx, foreign, DetailView); out != nil || err == nil || errors.Is(err, errRunState) {
		t.Fatalf("foreign record decoded before admission: %+v %v", out, err)
	}
	if out, err := s.Activities(ctx, []ids.ID{root.ID, idstest.N(99999)}); out != nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("partial missing batch: %+v %v", out, err)
	}
}

func TestWorkServiceChecksScopeAgainBeforePublishing(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"change", "run", "activity", "artifact", "edit"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			q, root, _ := activityFixture(t)
			ctx := context.Background()
			other := workflowTestProject(t, q.db, "foreign", "Foreign", "")
			foreign := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: other, WorkflowName: "secret", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(`{}`)}).ID
			artifact := storedTextArtifact(t, q.db, root.ID, "report", "private", "", "private bytes")
			auth := authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
				if p.Action == actionIdentify {
					return "reader", nil
				}
				if p.ProjectID != root.ProjectID {
					t.Fatalf("wrong scope %+v", p)
				}
				switch kind {
				case "change", "edit":
					// sql-guarantee: Move the change to a foreign project during authorization to test authorized-source revalidation; Change operations and generated transitions preserve project_id.
					completionExec(t, q.db, `UPDATE changes SET project_id=?,number=number+100000 WHERE id=?`, other, root.ChangeID)
				case "run", "activity":
					// sql-guarantee: Move the run to a foreign project during authorization while preserving its evidence identities; admission fixes project_id and no generated mutation reparents a run.
					completionExec(t, q.db, `UPDATE runs SET project_id=?,number=number+100000 WHERE id=?`, other, root.ID)
				case "artifact":
					// sql-guarantee: Move the already-authorized artifact to a foreign run before its body is read; artifact insertion fixes run_id and no generated mutation reparents an existing artifact.
					completionExec(t, q.db, `UPDATE artifacts SET run_id=? WHERE id=?`, foreign, artifact)
				}
				return "reader", nil
			})
			s := newService(q.db, auth, Runtime{})
			var err error
			switch kind {
			case "change":
				var out *Change
				out, err = s.Change(ctx, root.ChangeID)
				if out != nil {
					t.Fatal("changed-scope Change returned")
				}
			case "run":
				var out *RunSnapshot
				out, err = s.Snapshot(ctx, root.ID, DetailView)
				if out != nil {
					t.Fatal("changed-scope Run returned")
				}
			case "activity":
				var out map[ids.ID]Activity
				out, err = s.Activities(ctx, []ids.ID{root.ID})
				if out != nil {
					t.Fatal("changed-scope activity returned")
				}
			case "artifact":
				var out *ArtifactBody
				out, err = s.Artifact(ctx, artifact)
				if out != nil {
					t.Fatal("changed-scope bytes returned")
				}
			case "edit":
				title := "stolen"
				_, err = s.Edit(ctx, ChangeEdit{ID: root.ChangeID, Title: &title, ExpectedRevision: revision(0)})
				// sql-guarantee: a refused edit must leave no row with the title in any project; the scoped reads cannot prove absence outside the caller's view.
				completionCount(t, q.db, 0, `SELECT COUNT(*) FROM changes WHERE title='stolen'`)
			}
			if err == nil {
				t.Fatal("scope change accepted")
			}
		})
	}
}

func TestWorkCommandsUseCurrentAuthorityAndVerifiedReceiptActor(t *testing.T) {
	t.Parallel()
	q, root, _ := activityFixture(t)
	ctx := context.Background()
	allowed := true
	var seen []operationPermission
	s := newService(q.db, authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
		seen = append(seen, p)
		if !allowed {
			return "", &access.Denied{Reason: "revoked"}
		}
		return "verified-person", nil
	}), Runtime{})
	created, err := s.Create(ctx, ChangeDraft{ProjectID: root.ProjectID, Title: "from command"})
	if err != nil {
		t.Fatal(err)
	}
	title := "edited"
	if _, err := s.Edit(ctx, ChangeEdit{ID: created.ID, Title: &title, ExpectedRevision: revision(0)}); err != nil {
		t.Fatal(err)
	}
	wantReceiptCount(t, q.db, 2, func(e audit.Entry) bool {
		return e.Actor == "verified-person" && (e.Kind == "change.created" || e.Kind == "change.edited")
	})
	// A command-supplied project cannot broaden scope, even with an authority
	// that otherwise admits both projects.
	if _, err := s.Edit(ctx, ChangeEdit{ID: created.ID, ProjectID: idstest.N(999999), Title: &title, ExpectedRevision: revision(1)}); err == nil {
		t.Fatal("command scope replaced stored scope")
	}
	allowed = false
	if ch, err := s.Create(ctx, ChangeDraft{ProjectID: root.ProjectID, Title: "revoked"}); ch != nil || err == nil {
		t.Fatalf("revoked create: %+v %v", ch, err)
	}
	if ch, err := s.Edit(ctx, ChangeEdit{ID: created.ID, Title: &title, ExpectedRevision: revision(1)}); ch != nil || err == nil {
		t.Fatalf("revoked edit: %+v %v", ch, err)
	}
	// sql-guarantee: a refused create or edit must leave no row with the title in any project; the scoped reads cannot prove absence outside the caller's view.
	completionCount(t, q.db, 0, `SELECT COUNT(*) FROM changes WHERE title='revoked'`)
	if len(seen) < 3 || !reflect.DeepEqual(seen[0], operationPermission{Action: actionCreateChange, ProjectID: root.ProjectID}) || !reflect.DeepEqual(seen[2], operationPermission{Action: actionEditBrief, ProjectID: root.ProjectID, ChangeID: created.ID}) {
		t.Fatalf("operation scope: %+v", seen)
	}
}

func TestWorkArtifactChecksEvidencePermissionAndReleasesSnapshot(t *testing.T) {
	t.Parallel()
	q, root, _ := activityFixture(t)
	ctx := context.Background()
	artifact := storedTextArtifact(t, q.db, root.ID, "report", "private", "", "words")
	var seen []operationPermission
	s := newService(q.db, authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
		seen = append(seen, p)
		return "reader", nil
	}), Runtime{})
	q.db.Pool().SetMaxOpenConns(1)
	out, err := s.Artifact(ctx, artifact)
	if err != nil || out.Body != "words" {
		t.Fatalf("artifact: %+v %v", out, err)
	}
	if !reflect.DeepEqual(seen, []operationPermission{{Action: actionIdentify}, {Action: actionReadEvidence, ProjectID: root.ProjectID, RunID: root.ID}}) {
		t.Fatalf("wrong evidence authority: %+v", seen)
	}
	dbtest.FailNextStatement(t, q.db, "-- name: ReadArtifacts")
	if out, err := s.Artifact(ctx, artifact); out != nil || err == nil {
		t.Fatalf("partial artifact: %+v %v", out, err)
	}
	if _, err := readRun(ctx, q.db, root.ID); err != nil {
		t.Fatal(err)
	}
}

// Compile-time assertions keep the port independent of AppTown's identities.
var _ operationAuthority = authorityFunc(nil)
var _ database.Queryer = (*database.DB)(nil)

func (f authorityFunc) VisibleProjects(context.Context, commandAction) ([]ids.ID, error) {
	return nil, ErrCallerRequired
}

type projectAuthority struct {
	authorityFunc
	project ids.ID
}

func (f projectAuthority) VisibleProjects(ctx context.Context, action commandAction) ([]ids.ID, error) {
	if _, err := f.Require(ctx, operationPermission{Action: action, ProjectID: f.project}); err != nil {
		return nil, err
	}
	return []ids.ID{f.project}, nil
}

type aggregateAuthority struct {
	operationAuthority
	read func(context.Context, commandAction) ([]ids.ID, error)
}

func (f aggregateAuthority) VisibleProjects(ctx context.Context, action commandAction) ([]ids.ID, error) {
	return f.read(ctx, action)
}
