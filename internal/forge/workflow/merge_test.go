package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

type mergeEffectsTest struct {
	driver   scm.Driver
	observed func(scm.Result)
}

func (m mergeEffectsTest) Driver() scm.Driver                              { return m.driver }
func (m mergeEffectsTest) Observed(ctx context.Context, result scm.Result) { m.observed(result) }

type emptyMergeDriver struct{ scm.Driver }

func TestMergeOwnerSettlesOnlyTheGateObservedBeforeExternalWork(t *testing.T) {
	t.Parallel()
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "changed"}[changed], func(t *testing.T) {
			t.Parallel()
			q, run := mergeGateFixture(t)
			insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "merge", Type: "scm/merged", Status: StatusNeedsHuman.String(), ConfigJson: json.RawMessage(`{}`)})
			q.db.Pool().SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			calls := 0
			effects := mergeEffectsTest{driver: emptyMergeDriver{}, observed: func(result scm.Result) {
				calls++
				if !result.Merged || result.Source.RunID != run.ID {
					t.Fatalf("external observation %+v", result)
				}
				steps, err := readSteps(ctx, q.db, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(steps) != 1 || steps[0].Status != StatusNeedsHuman {
					t.Fatal("cleared before observing SCM")
				}
				if changed {
					// sql-guarantee: Merge settlement lockRunSnapshotIn compares the whole Run, including UpdatedAt; InvalidateRunDecision also stamps updated_at and could reject the snapshot without checking retry_revision, so change only retry_revision at this original interference point.
					completionExec(t, q.db, `UPDATE runs SET retry_revision=1 WHERE id=?`, run.ID)
				}
			}}
			service := scm.New(q.db, NewSCMWork(q.db, t.TempDir(), nil), &scm.RepositoryLocks{}, nil)
			owner := NewMerges(q.db, t.TempDir(), policyLawRules{}, service, effects)
			out, err := owner.Merge(ctx, run)
			if changed {
				if out.Allowed || !errors.Is(err, ErrRunMoved) {
					t.Fatalf("stale clearance: %+v %v", out, err)
				}
				wantStepCount(t, q.db, 1, func(s *Step) bool { return s.Status == StatusNeedsHuman })
				wantEventCount(t, q.db, 0, anyEvent)
			} else {
				if err != nil || !out.Allowed {
					t.Fatalf("clearance: %+v %v", out, err)
				}
				wantStepCount(t, q.db, 1, func(s *Step) bool { return s.Status == StatusPass })
				wantEventCount(t, q.db, 1, ofKind("check.evaluated"))
			}
			if calls != 1 {
				t.Fatalf("observations=%d", calls)
			}
		})
	}
}
