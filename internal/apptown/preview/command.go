package preview

import (
	"context"
	"errors"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/project"

	"github.com/flokkit/apptown/internal/access"
)

// Selection is a person's request for a change's preview; Restart boots it
// afresh.
type Selection struct {
	ChangeID ids.ID
	Restart  bool
}

// Console is a chat session's change, project and person, for a chat that
// asks to preview its own change.
type Console struct {
	ProjectID, ChangeID ids.ID
	Actor               string
}

// sources resolves a change and a console session through their owners'
// checked doors.
type sources interface {
	Change(context.Context, ids.ID) (Change, error)
	Console(context.Context, string) (Console, error)
	// Pin settles an authorized preview's source: a change a chat on the
	// worker holds is saved first and pinned to the saved commit. It runs
	// only after the request is authorized, since it has effects.
	Pin(context.Context, Change) (Change, error)
}

// ExecutionError is a preview that was admitted and failed to boot: the
// app's or the place it runs, never a refused request.
type ExecutionError struct{ Cause error }

func (e *ExecutionError) Error() string { return e.Cause.Error() }
func (e *ExecutionError) Unwrap() error { return e.Cause }

// RequestChangePreview boots a change's preview for a verified person who
// may preview the change (a chat only its own), from the change's source as
// Work resolves it, inside the change's boot interval.
func (s *Service) RequestChangePreview(ctx context.Context, in Selection) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if s.authority == nil || s.sources == nil {
		return Result{}, errors.New("change previews are not configured")
	}
	caller, _ := access.CallerFrom(ctx)
	pr := caller.Principal
	if !caller.Verified || pr.Name == "" {
		return Result{}, access.ErrPrincipalRequired
	}
	if pr.Instrument == "console" {
		session, err := s.sources.Console(ctx, pr.ConsoleID)
		if err != nil {
			return Result{}, err
		}
		if session.ChangeID != in.ChangeID || session.ProjectID != pr.ProjectID || session.Actor != pr.Person {
			return Result{}, &access.Denied{Reason: "a console can preview only its own change"}
		}
	}
	ch, err := s.sources.Change(ctx, in.ChangeID)
	if err != nil {
		return Result{}, err
	}
	if ch.ID != in.ChangeID || ch.ProjectID.IsZero() {
		return Result{}, errors.New("invalid authorized preview selection")
	}
	if _, err := s.authority.Require(ctx, access.Permission{Capability: access.CapChangePreview, Resource: access.Resource{Type: "change", ID: in.ChangeID.String(), ChangeID: in.ChangeID, ProjectID: ch.ProjectID}, Context: access.Context{Consequential: true, Route: "deployment.preview"}}); err != nil {
		return Result{}, err
	}
	// A project that is not active boots no new preview. A preview holds no
	// project lock while it boots, so this is a read: one admitted just
	// before a deletion is removed with the project's previews.
	if l, err := project.LifecycleIn(ctx, s.db, ch.ProjectID); err != nil {
		return Result{}, err
	} else if l.State != project.Active {
		return Result{}, &project.NotActiveError{ProjectID: ch.ProjectID, State: l.State}
	}
	if ch, err = s.sources.Pin(ctx, ch); err != nil {
		return Result{}, err
	}
	defer s.requestObservation(ctx, ch.ID)
	out, err := s.runtime.bootChange(ctx, ch, in.Restart, s.host)
	if err != nil {
		return Result{}, &ExecutionError{Cause: err}
	}
	return out, nil
}
