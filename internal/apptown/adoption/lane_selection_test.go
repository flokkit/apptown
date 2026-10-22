package adoption

import (
	"context"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
)

// A lane selection and the adoption revision it makes commit together and
// name the person; a failed revision leaves no selection behind, and a
// selection that changes nothing records nothing.
func TestLaneSelectionAndItsRevisionCommitTogether(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := adoptionTestProject(t, db, "lane-select", "Lane selection")
	if err := db.InTx(context.Background(), func(tx *database.Tx) error { return InitializeIn(t.Context(), tx, pid, 0, "", "") }); err != nil {
		t.Fatal(err)
	}
	ways := wow.New(db, wow.Dependencies{})
	if _, _, err := ways.PublishWorkflow(t.Context(), fmt.Sprintf(laneYAML, "picked", "picked", ""), "alice"); err != nil {
		t.Fatal(err)
	}
	s := New(db, Dependencies{Ways: ways})

	state := func() (bool, []RecordedRevision) {
		t.Helper()
		on, err := s.LaneSelected(t.Context(), pid, "picked")
		if err != nil {
			t.Fatal(err)
		}
		history, err := s.History(t.Context(), pid, 10)
		if err != nil {
			t.Fatal(err)
		}
		return on, history
	}

	if err := s.SelectLane(t.Context(), pid, "picked", true, ""); err == nil {
		t.Fatal("a selection naming no one was recorded")
	}
	dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
	if err := s.SelectLane(t.Context(), pid, "picked", true, "alice"); err == nil {
		t.Fatal("a selection whose history failed succeeded")
	}
	if on, history := state(); on || len(history) != 0 {
		t.Fatalf("a failed selection left a trace: selected %v, history %+v", on, history)
	}

	if err := s.SelectLane(t.Context(), pid, "picked", true, "alice"); err != nil {
		t.Fatal(err)
	}
	on, history := state()
	if !on || len(history) != 1 || history[0].CreatedBy != "alice" || history[0].Reason != "lane picked selected" {
		t.Fatalf("selection and its revision: selected %v, history %+v", on, history)
	}

	if err := s.SelectLane(t.Context(), pid, "picked", true, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, history := state(); len(history) != 1 {
		t.Fatalf("a selection that changed nothing recorded a revision: %+v", history)
	}
}
