package conversation

import (
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// On an AppTown with no model a chat is refused when it is asked for, with
// the sentence workflow admission uses: its agent would otherwise start and
// fail every message ("the helper's screen never accepted the message").
// The refusal comes first: this admission has no sessions, worker or
// workspace owners to reach, so getting past it would fail differently.
func TestAChatIsRefusedWhenNoModelIsSetUp(t *testing.T) {
	t.Parallel()
	admission := &Admission{models: model.NewSelection(model.New(databaseSuite.New(t)), "")}
	_, err := admission.Open(t.Context(), &project.Project{ID: idstest.N(1), Slug: "game"}, ProjectOpen{}, "owner")
	var refused *AdmissionConflict
	if !errors.As(err, &refused) || err.Error() != model.NoModelRefusal {
		t.Fatalf("a chat with no model: %v", err)
	}
}
