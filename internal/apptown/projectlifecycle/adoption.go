package projectlifecycle

import (
	"context"
	"database/sql"
	"errors"
	"log"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
)

// Adopt writes the candidate as the project's current revision:
// one transaction under the project row's lock, refused unless the
// project is still on the revision the candidate was prepared from.
// Serialised with merges through the injected project repository lock. Returns the new revision.
func (s *Service) Adopt(ctx context.Context, p *project.Project, c *adoption.Candidate, who, reason string) (int64, error) {
	return s.applyCandidateForChange(ctx, p, c, who, reason, ids.Nil, adoption.OperationSelect)
}
func (s *Service) applyCandidateForChange(ctx context.Context, p *project.Project, c *adoption.Candidate, who, reason string, changeID ids.ID, operation adoption.Operation) (int64, error) {
	if s.superseded == nil {
		return 0, errors.New("closing superseded adoption changes is unavailable")
	}
	// The closure is the project's repository lock, held across the adoption
	// and the abandonments it implies.
	closure, err := scm.PrepareClosure(ctx, s.repositories, p.ID)
	if err != nil {
		return 0, err
	}
	defer closure.Close()
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
		if err := s.closeSupersededIn(ctx, tx, closure, p.ID, who); err != nil {
			return err
		}
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

// closeSupersededIn: a changed pack set retires the uplifts aimed at the old
// one; they could never pass, so they close in the act that changed it.
func (s *Service) closeSupersededIn(ctx context.Context, tx *database.Tx, closure *scm.Closure, projectID ids.ID, who string) error {
	set, err := adoption.ProjectPacksIn(ctx, tx, projectID)
	if err != nil {
		return err
	}
	gone, reason := adoption.SupersededBy(set)
	_, err = s.superseded.CloseSupersededUpliftsIn(ctx, tx, closure, projectID, who, gone, reason)
	return err
}

// PullPack moves the project's membership of a pack to its latest version
// (adoption.PullPack), closing in the same transaction the uplifts that
// version leaves behind.
func (s *Service) PullPack(ctx context.Context, p *project.Project, pack wow.Pack, who string) (adoption.Pulled, error) {
	if s.superseded == nil {
		return adoption.Pulled{}, errors.New("closing superseded adoption changes is unavailable")
	}
	closure, err := scm.PrepareClosure(ctx, s.repositories, p.ID)
	if err != nil {
		return adoption.Pulled{}, err
	}
	defer closure.Close()
	return s.adoption.PullPack(ctx, p.ID, pack, who, func(tx *database.Tx) error {
		return s.closeSupersededIn(ctx, tx, closure, p.ID, who)
	})
}
