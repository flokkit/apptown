package workflow

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestCompletionTaskChecksStateAndClassifiesFailures(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	service := newService(q.db, allowProject(run.ProjectID), Runtime{})
	ctx := context.Background()
	key := fmt.Sprintf("%d/narrative", run.ID)
	if _, err := service.FollowUp(ctx, key); !errors.Is(err, ErrCompletionTask) {
		t.Fatalf("unfinished Run admitted: %v", err)
	}
	transitionStoredRunForTest(t, q.db, run.ID, "", RunDone)
	for _, effect := range []CompletionEffect{CompletionCleanup, CompletionNarrative, CompletionAdopt, CompletionLand, CompletionParent} {
		task, err := service.FollowUp(ctx, fmt.Sprintf("%d/%s", run.ID, effect))
		if err != nil || task.Removed || task.Run.ID != run.ID || task.Effect != effect {
			t.Fatalf("task %+v %v", task, err)
		}
	}
	for _, key := range []string{"missing-separator", idstest.N(999999).String() + "/unknown", "no-number/parent", fmt.Sprintf("%d/unknown", run.ID)} {
		if _, err := service.FollowUp(ctx, key); !errors.Is(err, ErrCompletionTask) {
			t.Fatalf("invalid task %q: %v", key, err)
		}
	}
	missing, err := service.FollowUp(ctx, idstest.N(999999).String()+"/cleanup")
	if err != nil || !missing.Removed || missing.Run != nil {
		t.Fatalf("removed Run: %+v %v", missing, err)
	}
	dbtest.FailNextStatement(t, q.db, "-- name: RunByID")
	_, err = service.FollowUp(ctx, key)
	var sqlError interface{ SQLState() string }
	if !errors.As(err, &sqlError) || sqlError.SQLState() != "42883" || errors.Is(err, ErrCompletionTask) {
		t.Fatalf("injected SQL read error was lost or classified as permanent: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := service.FollowUp(cancelled, key); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled task: %v", err)
	}
}
func TestCompletionTaskDerivesPreviewCleanupFromPinnedRun(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	service := newService(q.db, allowProject(run.ProjectID), Runtime{})
	ctx := context.Background()
	transitionStoredRunForTest(t, q.db, run.ID, "", RunDone)
	key := fmt.Sprintf("%d/cleanup", run.ID)
	task, err := service.FollowUp(ctx, key)
	if err != nil || !task.PreviewChangeID.IsZero() {
		t.Fatalf("working preview: %+v %v", task, err)
	}
	// sql-guarantee: the same completed run is reread first as working and then landing; declarations are immutable after admission and no generated mutation changes only definition_json.
	completionExec(t, q.db, `UPDATE runs SET definition_json='{"kind":"pipeline","role":"landing","stages":[{"name":"finish"}]}' WHERE id=?`, run.ID)
	task, err = service.FollowUp(ctx, key)
	if err != nil || task.PreviewChangeID != run.ChangeID {
		t.Fatalf("root landing: %+v %v", task, err)
	}
	// sql-guarantee: the cleanup projection must reread the same run after only parent identity changes; parent linkage is immutable and has no generated mutation.
	completionExec(t, q.db, `UPDATE runs SET parent_run_id='00000000-0007-7000-8000-000000000000' WHERE id=?`, run.ID)
	task, err = service.FollowUp(ctx, key)
	if err != nil || !task.PreviewChangeID.IsZero() {
		t.Fatalf("child cleanup: %+v %v", task, err)
	}
}

func TestCompletionFollowUpRequiresAuthorityAndRetainsScope(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	transitionStoredRunForTest(t, q.db, run.ID, "", RunDone)
	key := fmt.Sprintf("%d/cleanup", run.ID)
	denied := errors.New("not an engine caller")
	service := newService(q.db, authorityFunc(func(context.Context, operationPermission) (string, error) { return "", denied }), Runtime{})
	if task, err := service.FollowUp(context.Background(), key); !errors.Is(err, denied) || task.Run != nil {
		t.Fatalf("unauthorized task: %+v %v", task, err)
	}
	other := workflowTestProject(t, q.db, "other", "Other", "")
	service.authority = authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
		if p.Action == actionAdvanceRun {
			// sql-guarantee: authority admits the original completion task before only its project identity moves; no Workflow mutation exposes project reassignment.
			completionExec(t, q.db, `UPDATE runs SET project_id=?,number=number+100000 WHERE id=?`, other, run.ID)
		}
		return "engine", nil
	})
	if task, err := service.FollowUp(context.Background(), key); !errors.Is(err, ErrRunMoved) || task.Run != nil {
		t.Fatalf("moved task: %+v %v", task, err)
	}
}
