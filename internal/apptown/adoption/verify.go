package adoption

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/ids"
	"log"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/database"
)

// Drift is a project whose live rows moved outside the adoption door.
type Drift struct {
	Revision int64  // the current revision, which stays current
	Diff     string // from the revision's receipt to the live rows
}

// VerifyRevision is the boot barrier's second half for one project: after
// the syncs, its live state is resolved again against its current revision.
// A difference is drift — something moved the project outside the adoption
// door — and is quarantined, never absorbed: the rows are wrong, not the
// receipt, so gates and steering keep reading the receipt and the project
// carries a drift note (with a receipt) until a person saves what it should
// be on. No difference clears any note. A project without a current
// revision, or whose state cannot be read, is skipped (nil).
func (s *Service) VerifyRevision(ctx context.Context, projectID ids.ID) *Drift {
	prior, err := CurrentReceiptIn(ctx, s.db, projectID)
	if _, err := database.Lookup(err); err != nil {
		log.Printf("read failed: %v", err)
	}
	receipt, err := s.Union(ctx, projectID, prior)
	if err != nil || prior == nil {
		return nil
	}
	if Digest(receipt) == prior.Digest {
		if err := s.ClearDrift(ctx, projectID); err != nil {
			log.Printf("project %d: clearing the drift note: %v", projectID, err)
		}
		return nil
	}
	// Quarantined, never absorbed: the receipt stays current.
	diff := Diff(prior.Receipt, receipt)
	if err := s.NoteDrift(ctx, projectID, diff); err != nil {
		log.Printf("project %d: noting drift: %v", projectID, err)
	}
	audit.At(s.adoptions.now()).BestEffort(ctx, s.db, "apptown-system", projectID, "adoption.drift", fmt.Sprintf("r%d", prior.Revision), map[string]any{"diff": diff})
	return &Drift{Revision: prior.Revision, Diff: diff}
}
