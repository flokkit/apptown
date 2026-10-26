package deployment

import (
	"context"
	"github.com/flokkit/apptown/internal/ids"
)

// OperationalObservation joins current local intent with matching cached
// runtime and exact-generation hold evidence. It performs no provider reads.
type OperationalObservation struct {
	ID                                    ids.ID
	Name, DisplayLabel, UpdatedAt         string
	Kind                                  Kind
	DesiredGeneration, ObservedGeneration int64
	Readiness                             Readiness
	Runtime                               RuntimeObservation
	Hold                                  *CrashLoopHold
}

func (s *Service) Operational(ctx context.Context, projectID ids.ID) ([]OperationalObservation, error) {
	records, err := s.authorizedListing(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]OperationalObservation, 0, len(records))
	for _, r := range records {
		d := r.deployment
		hold, err := s.crashLoopHold(ctx, d)
		if err != nil {
			return nil, err
		}
		out = append(out, OperationalObservation{ID: d.ID, Name: d.Name, DisplayLabel: d.DisplayLabel, UpdatedAt: d.UpdatedAt, Kind: d.Kind, DesiredGeneration: d.DesiredGeneration, ObservedGeneration: d.ObservedGeneration, Readiness: d.Readiness, Runtime: s.observations.read(r, s.now()), Hold: hold})
	}
	return out, ctx.Err()
}
