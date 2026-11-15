package workflow

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"

	"github.com/flokkit/apptown/internal/ids"
)

// landingAvailability supplies the current project's configured landing entry.
// Creation still checks the accepted source and selected definition in Starter.
type landingAvailability interface {
	CanLand(context.Context, ids.ID) (bool, error)
}

type acceptedLandingRuntime struct {
	Availability landingAvailability
	Starter      *starter
	Advancement  *advancer
}

type landingExecution struct {
	RunID   ids.ID
	Advance *AdvanceResult
}

// advanceAccepted performs one attempt. The persisted landing link, acceptance
// receipt and scoped readiness make an explicit retry safe without polling.
func (s landings) advanceAccepted(ctx context.Context, workID ids.ID, runtime acceptedLandingRuntime) (landingExecution, error) {
	var out landingExecution
	pending, err := s.pending(ctx, workID)
	if err != nil || pending.Superseded {
		return out, err
	}
	landingID := pending.LandingID
	if landingID.IsZero() {
		ch, err := readChange(ctx, s.db, pending.Work.ChangeID)
		if err != nil {
			return out, err
		}
		if ch.ProjectID != pending.Work.ProjectID {
			return out, ErrRunMoved
		}
		if ch.State != ChangeInProgress {
			return out, nil
		}
		if runtime.Availability == nil {
			return out, fmt.Errorf("landing availability is unavailable")
		}
		allowed, err := runtime.Availability.CanLand(ctx, ch.ProjectID)
		if err != nil || !allowed {
			return out, err
		}
		if runtime.Starter == nil {
			return out, fmt.Errorf("landing starter is unavailable")
		}
		landing, err := runtime.Starter.start(ctx, StartRequest{ProjectID: ch.ProjectID, ChangeID: ch.ID, WorkflowName: "land", InputsJSON: "{}", acceptedRunID: pending.Work.ID})
		if landing != nil {
			out.RunID = landing.ID
		}
		if err != nil {
			return out, err
		}
		landingID = landing.ID
	}
	advance, err := s.apply(ctx, pending.Work.ID, landingID)
	if err != nil || advance == nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if runtime.Starter == nil || runtime.Advancement == nil {
		return out, fmt.Errorf("landing execution is unavailable")
	}
	if err = runtime.Starter.prepareScoped(ctx, advance); err != nil {
		return out, err
	}
	out.RunID = advance.ID
	out.Advance, err = runtime.Advancement.advanceRun(ctx, advance, "system")
	return out, err
}

// AdvanceAcceptedLanding performs one attempt at moving accepted work to
// its landing run, starting the landing if none exists, and dispatches
// what the attempt readied. Availability is the caller's say on whether
// the project may land right now.
func (s *Service) AdvanceAcceptedLanding(ctx context.Context, workID ids.ID, availability landingAvailability) (landingExecution, error) {
	out, err := s.landings.advanceAccepted(ctx, workID, acceptedLandingRuntime{Availability: availability, Starter: s.starter, Advancement: s.advancer})
	s.dispatchAdvanced(out.RunID, out.Advance, err)
	return out, err
}

// OldestQueuedAcceptedWork reads one project's next accepted landing without
// reserving it. Admission serializes and rechecks the accepted-work identity.
func (s *Service) OldestQueuedAcceptedWork(ctx context.Context, project ids.ID) (ids.ID, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionReadProject, ProjectID: project}); err != nil {
		return ids.Nil, err
	}
	return oldestQueuedAcceptedWork(ctx, s.db, project, ids.Nil)
}

func oldestQueuedAcceptedWork(ctx context.Context, db database.Queryer, project, change ids.ID) (ids.ID, error) {
	rows, err := workflowsql.New(db.SQL()).OldestQueuedAcceptedWork(ctx, workflowsql.OldestQueuedAcceptedWorkParams{ProjectID: project, ChangeID: change})
	if err != nil || len(rows) == 0 {
		return ids.Nil, err
	}
	return rows[0], nil
}

// A direct Land request has already checked the caller's landing authority;
// the starter resolves and validates the configured landing itself.
type requestedLanding struct{}

func (requestedLanding) CanLand(context.Context, ids.ID) (bool, error) { return true, nil }
