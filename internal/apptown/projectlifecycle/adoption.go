package projectlifecycle

import (
	"context"
	"database/sql"
	"errors"
	"log"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

// Adopt writes the candidate as the project's current revision:
// one transaction under the project row's lock, refused unless the
// project is still on the revision the candidate was prepared from.
// Serialised with merges through the injected project repository lock. Returns the new revision.
func (s *Service) Adopt(ctx context.Context, p *project.Project, c *adoption.Candidate, who, reason string, operation adoption.Operation) (int64, error) {
	return s.applyCandidateForChange(ctx, p, c, who, reason, ids.Nil, operation)
}
func (s *Service) applyCandidateForChange(ctx context.Context, p *project.Project, c *adoption.Candidate, who, reason string, changeID ids.ID, operation adoption.Operation) (int64, error) {
	release, err := s.repositories.Acquire(ctx, p.ID)
	if err != nil {
		return 0, err
	}
	defer release()
	var newRev int64
	var applied bool
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		// Preserve the project-first lock and the Work marker's idempotence. A
		// completed marker wins over an obsolete candidate; Adoption owns the
		// expected-revision check for every actual transition.
		_, err := adoption.CurrentForUpdateIn(ctx, tx, p.ID)
		if errors.Is(err, sql.ErrNoRows) {
			if err := adoption.InitializeIn(ctx, tx, p.ID, 0, ""); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if !changeID.IsZero() {
			adopted, err := workflow.UpliftAdoptionIn(ctx, tx, workflow.ChangeRef{ID: changeID, ProjectID: p.ID})
			if err != nil {
				return err
			}
			if adopted != 0 {
				newRev = adopted
				return nil
			}
		}
		// Stored Change targets omit their scope; the guarded Work observation
		// supplies it here rather than trusting transport JSON.
		scoped := *c
		scoped.ProjectID = p.ID
		next, err := s.adoption.AdoptIn(ctx, tx, p.ID, scoped, c.ExpectedRevision, who, reason, operation)
		if err != nil {
			return err
		}
		newRev, applied = next.Revision, true
		if !changeID.IsZero() {
			if err := workflow.StampUpliftAdoptionIn(ctx, tx, workflow.UpliftAdoption{Change: workflow.ChangeRef{ID: changeID, ProjectID: p.ID}, Revision: newRev}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if applied {
		log.Printf("adoption: %s — %s (revision %d)", p.Slug, reason, newRev)
	}
	return newRev, nil
}

// SettleUplift commits the landed Change's adoption and its marker together.
// A replay with a committed marker returns its revision before checking an old
// candidate. Callers validate the Change's sealed target before settlement.
func (s *Service) SettleUplift(ctx context.Context, p *project.Project, c *adoption.Candidate, who, reason string, changeID ids.ID) (int64, error) {
	return s.applyCandidateForChange(ctx, p, c, who, reason, changeID, adoption.OperationSelect)
}
