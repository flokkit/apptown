package conversation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// On an AppTown with no model a chat is refused when it is asked for, with
// the sentence workflow admission uses: its agent would otherwise start and
// fail every message ("the helper's screen never accepted the message").
// The refusal comes first: this admission has no sessions, worker or
// workspace owners to reach, so getting past it would fail differently.
func TestAChatIsRefusedWhenNoModelIsSetUp(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	id, err := database.Transact(t.Context(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "game", Name: "Game"})
	})
	if err != nil {
		t.Fatal(err)
	}
	admission := &Admission{models: model.NewSelection(model.New(db))}
	_, err = admission.Open(t.Context(), &project.Project{ID: id, Slug: "game"}, ProjectOpen{}, "owner")
	var refused *admissionConflict
	if !errors.As(err, &refused) || err.Error() != model.NoModelRefusal {
		t.Fatalf("a chat with no model: %v", err)
	}
}

// Availability is a read: it reserves no session, creates no change and
// never asks the worker anything (no registry probe, no wait on a worker that
// does not answer). Opening the chat is where placement is checked.
func TestChangeChatAvailabilityNeedsAModelAndAPlaceToRun(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	slots := model.New(db)
	admission := &Admission{models: model.NewSelection(slots), noLocal: true}
	if got, err := admission.ChangeChatAvailable(t.Context(), ids.Nil); err != nil || got {
		t.Fatalf("fresh install: %t %v", got, err)
	}
	if err := slots.SetDefault(t.Context(), "local/chat"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		noLocal bool
		worker  WorkerPlacement
		want    bool
	}{
		{"local", false, nil, true}, {"no runtime", true, nil, false},
		{"worker", true, unaskedWorker{}, true},
		{"worker beside local", false, unaskedWorker{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			admission.noLocal, admission.workers = tc.noLocal, tc.worker
			got, err := admission.ChangeChatAvailable(t.Context(), ids.Nil)
			if err != nil || got != tc.want {
				t.Fatalf("availability: %t %v", got, err)
			}
		})
	}
}

// unaskedWorker fails the test if availability asks it anything.
type unaskedWorker struct{}

func (unaskedWorker) Ready(context.Context) error {
	panic("availability must not probe the worker")
}
func (unaskedWorker) Publish(context.Context, *project.Project, *workflow.Change, string) error {
	panic("availability must not publish")
}
