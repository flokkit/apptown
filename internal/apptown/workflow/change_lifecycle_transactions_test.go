package workflow

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

type changeTransitionAuthority struct{}

func (a changeTransitionAuthority) Require(ctx context.Context, _ access.Permission) (access.Caller, error) {
	caller, _ := access.CallerFrom(ctx)
	return caller, nil
}

func changeTransitionContext(ctx context.Context, who string) context.Context {
	return access.WithCaller(ctx, access.Caller{Principal: access.Principal{Kind: "user", Name: who, Via: "open"}, Verified: true, Receipt: who})
}

func TestChangeParticipantRequiresProjectAndOwnedRoot(t *testing.T) {
	t.Parallel()
	w, run, _ := completionFixture(t, "working")
	other := workflowTestProject(t, w, "other", "Other", "")
	for _, tc := range []struct {
		name    string
		project ids.ID
		move    ChangeMove
		missing bool
	}{
		{"foreign project", other, ChangeMove{Action: ShipChange}, true},
		{"missing root", run.ProjectID, ChangeMove{Action: attachChangeRun, RunID: idstest.N(99999)}, false},
		{"absent run", run.ProjectID, ChangeMove{Action: LandChange}, false},
		{"unexpected run", run.ProjectID, ChangeMove{Action: ShipChange, RunID: run.ID}, false},
	} {
		err := w.InTx(context.Background(), func(tx *database.Tx) error {
			if _, err := workflowsql.New(tx.SQL()).ProjectForUpdate(context.Background(), tc.project); err != nil {
				return err
			}
			_, err := TransitionChangeIn(context.Background(), tx, tc.project, run.ChangeID, tc.move, time.Now())
			return err
		})
		if err == nil || tc.missing && !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("%s accepted: %v", tc.name, err)
		}
	}
	for _, mutation := range []string{"project_id", "change_id", "parent_run_id"} {
		// sql-guarantee: Independently corrupt project_id, change_id or parent_run_id on the same candidate root to test attachment scope; existing run mutations preserve these admission identities.
		completionExec(t, w, `UPDATE runs SET `+mutation+`=$1 WHERE id=$2`, other, run.ID)
		err := w.InTx(context.Background(), func(tx *database.Tx) error {
			if _, err := workflowsql.New(tx.SQL()).ProjectForUpdate(context.Background(), run.ProjectID); err != nil {
				return err
			}
			_, err := TransitionChangeIn(context.Background(), tx, run.ProjectID, run.ChangeID, ChangeMove{Action: attachChangeRun, RunID: run.ID}, time.Now())
			return err
		})
		if !errors.Is(err, ErrChangeTransition) {
			t.Fatalf("foreign %s attached: %v", mutation, err)
		}
		// sql-guarantee: Restore the original candidate's three admission identities between independent attachment-fence cases; no generated run mutation can reparent or repair these fields.
		completionExec(t, w, `UPDATE runs SET project_id=$1,number=number+100000,change_id=$2,parent_run_id=NULL WHERE id=$3`, run.ProjectID, run.ChangeID, run.ID)
	}
	ch := seenChange(t, w, run.ChangeID)
	expect(t, ch.RunID.IsZero() && ch.State == "in_progress", "refused attachment moved the change: %+v", ch)
}

// A stored state outside the lifecycle is refused on read, including
// "proposed", the backlog state AppTown no longer has.
func TestChangeReadsRejectInvalidStoredState(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"unexpected", "proposed"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			store, run, _ := completionFixture(t, "working")
			testAccounts(t, store, "alice")
			transitionScopeChange(t, store, workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, State: state})
			if got, err := ReadChangeIn(context.Background(), store, run.ChangeID); got != nil || !errors.Is(err, errChangeState) {
				t.Fatalf("invalid record escaped: %+v %v", got, err)
			}
			if got, err := New(context.Background(), store, Dependencies{Authority: changeTransitionAuthority{}}).Changes(changeTransitionContext(context.Background(), "alice"), run.ProjectID, ChangeList{}); got != nil || !errors.Is(err, errChangeState) {
				t.Fatalf("invalid list escaped: %+v %v", got, err)
			}
		})
	}
}

// This command-only double deliberately cannot authorize aggregate reads.
func (changeTransitionAuthority) VisibleProjects(context.Context, access.Capability) ([]ids.ID, error) {
	return nil, access.ErrAuthorityUnavailable
}
