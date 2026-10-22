package conversation

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// ProjectOpen names a new session or continuation, before runtime admission.
type ProjectOpen struct {
	Model        string `json:"model"`
	ChangeID     ids.ID `json:"changeId"`
	ContinueStep ids.ID `json:"continueStep"`
	// Placement is where the chat runs: PlaceOnWorker or PlaceOnAppTown.
	// Empty chooses the worker for a change's chat when worker chats are
	// on and the worker can serve it, else AppTown.
	Placement string `json:"placement,omitempty"`
}

var ErrNoChange = fault.New(fault.NotFound, "no such change in this project")

// AdmissionConflict refuses an open without turning it into a runtime failure.
// Code names a refusal a client answers specially (CodeChatOnAppTown), empty
// for the rest.
type AdmissionConflict struct {
	Err  error
	Code string
}

func (e *AdmissionConflict) Error() string         { return e.Err.Error() }
func (e *AdmissionConflict) FaultKind() fault.Kind { return fault.Conflict }
func (e *AdmissionConflict) FaultCode() string     { return e.Code }
func (e *AdmissionConflict) Unwrap() error         { return e.Err }

// CodeChatOnAppTown marks a refusal to open a change's chat on the worker that
// the same chat opened on AppTown would not meet: the page offers that
// instead of reading the sentence.
const CodeChatOnAppTown = "chat-on-apptown"

// workerRefusal refuses a chat on the worker. It names AppTown as the way
// round, in the sentence and the code, only when this apptown runs chats
// itself: an AppTown that runs none must not send the reader there.
func (c *Admission) workerRefusal(err error, onAppTown string) *AdmissionConflict {
	if c.noLocal {
		return &AdmissionConflict{Err: err}
	}
	return &AdmissionConflict{Err: fmt.Errorf("%w; %s", err, onAppTown), Code: CodeChatOnAppTown}
}

// ReserveProject is the admission an opening chat holds: the
// console's reservation, then the project verified to exist so a stale
// request after a completed purge is refused.
func ReserveProject(ctx context.Context, sessions *console.Service, db database.Queryer, projectID ids.ID) (func(), error) {
	release, err := sessions.Reserve(ForProject(projectID).Labels)
	if err != nil {
		return nil, err
	}
	if _, err := project.ByIDIn(ctx, db, projectID); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
