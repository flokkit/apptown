package workflow

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"
)

// LandingAvailability supplies the current project's configured landing entry.
// Creation still checks the accepted source and selected definition in Starter.
type LandingAvailability interface {
	CanLand(context.Context, ids.ID) (bool, error)
}

type acceptedLandingRuntime struct {
	Availability LandingAvailability
	Starter      *starter
	Advancement  *advancer
}

type LandingExecution struct {
	RunID   ids.ID
	Advance *AdvanceResult
}

// AdvanceAccepted performs one attempt. The persisted landing link, acceptance
// receipt and scoped readiness make an explicit retry safe without polling.
func (s landings) AdvanceAccepted(ctx context.Context, workID ids.ID, runtime acceptedLandingRuntime) (LandingExecution, error) {
	var out LandingExecution
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
		landing, err := runtime.Starter.Start(ctx, StartRequest{ProjectID: ch.ProjectID, ChangeID: ch.ID, WorkflowName: "land", InputsJSON: "{}", AcceptedRunID: pending.Work.ID})
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
	if err = runtime.Starter.PrepareScoped(ctx, advance); err != nil {
		return out, err
	}
	out.RunID = advance.ID
	out.Advance, err = runtime.Advancement.AdvanceRun(ctx, advance, "system")
	return out, err
}

// AdvanceAcceptedLanding performs one attempt at moving accepted work to
// its landing run, starting the landing if none exists, and dispatches
// what the attempt readied. Availability is the caller's say on whether
// the project may land right now.
func (s *Service) AdvanceAcceptedLanding(ctx context.Context, workID ids.ID, availability LandingAvailability) (LandingExecution, error) {
	out, err := s.landings.AdvanceAccepted(ctx, workID, acceptedLandingRuntime{Availability: availability, Starter: s.starter, Advancement: s.advancer})
	s.dispatchAdvanced(out.RunID, out.Advance, err)
	return out, err
}
