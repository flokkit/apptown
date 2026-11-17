package deployment

import (
	"context"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/fault"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

// OperationalObservation joins current local intent with matching cached
// runtime and exact-generation hold evidence. It performs no provider reads.
type OperationalObservation struct {
	RuntimeState string
	LastApplied  *AppliedAttempt
	// lastActAt is when the deployment's own last act ended: a reading
	// begun earlier cannot say what runs after it.
	LastActAt time.Time
	// ReadingDue is when a runtime reading begun after LastActAt should have
	// been published, by the observer's schedule; zero with no act.
	ReadingDue                            time.Time
	ID, TargetID                          ids.ID
	Name, DisplayLabel, UpdatedAt         string
	Kind                                  Kind
	DesiredGeneration, ObservedGeneration int64
	Readiness                             Readiness
	Runtime                               RuntimeObservation
	Hold                                  *CrashLoopHold
}

// Operational checks deployment visibility once for the entire requested set.
func (s *Service) Operational(ctx context.Context, projects []ids.ID) (map[ids.ID][]OperationalObservation, error) {
	authority, ok := s.authority.(access.ReadAuthorizer)
	if !ok {
		return nil, access.ErrAuthorityUnavailable
	}
	visible, err := authority.VisibleProjects(ctx, access.CapDeploymentRead)
	if err != nil {
		return nil, err
	}
	allowed := map[ids.ID]bool{}
	for _, id := range visible {
		allowed[id] = true
	}
	for _, id := range projects {
		if !allowed[id] {
			return nil, fault.New(fault.Forbidden, "project is not readable")
		}
	}
	records, err := s.listRecords(ctx, projects)
	if err != nil {
		return nil, err
	}
	out := map[ids.ID][]OperationalObservation{}
	for _, id := range projects {
		out[id] = []OperationalObservation{}
	}
	for _, r := range records {
		rows, err := s.operational(ctx, []listingRecord{r})
		if err != nil {
			return nil, err
		}
		out[r.deployment.ProjectID] = append(out[r.deployment.ProjectID], rows...)
	}
	return out, ctx.Err()
}

func (s *Service) operational(ctx context.Context, records []listingRecord) ([]OperationalObservation, error) {
	out := make([]OperationalObservation, 0, len(records))
	for _, r := range records {
		d := r.deployment
		hold, err := s.crashLoopHold(ctx, d)
		if err != nil {
			return nil, err
		}
		var due time.Time
		if at := d.lastActAt(); !at.IsZero() {
			due = at.Add(firstReadingWithin)
		}
		out = append(out, OperationalObservation{ReadingDue: due, RuntimeState: d.RuntimeState, LastApplied: r.summary.LastApplied, LastActAt: d.lastActAt(), ID: d.ID, TargetID: d.TargetID, Name: d.Name, DisplayLabel: d.DisplayLabel, UpdatedAt: d.UpdatedAt, Kind: d.Kind, DesiredGeneration: d.DesiredGeneration, ObservedGeneration: d.ObservedGeneration, Readiness: d.Readiness, Runtime: s.observations.read(r, s.now()), Hold: hold})
	}
	return out, ctx.Err()
}
