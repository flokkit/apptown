package workcommands

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

// UpliftSettlement applies a shipped Change's exact adoption target.
type UpliftSettlement struct {
	DB        *database.DB
	Adoption  *adoption.Service
	Lifecycle *projectlifecycle.Service
}

// Complete moves a shipped change's project to its target.
// Idempotent: a change already adopted is left alone.
func (a UpliftSettlement) Complete(ctx context.Context, changeID ids.ID) error {
	ch, err := workflow.ReadChangeIn(ctx, a.DB, changeID)
	if err != nil {
		return err
	}
	if ch.Type != "uplift" || ch.AdoptedRevision != 0 {
		return nil
	}
	p, projectErr := project.ByIDIn(ctx, a.DB, ch.ProjectID)
	if projectErr != nil {
		return fmt.Errorf("project %d is unavailable", ch.ProjectID)
	}
	var c adoption.Candidate
	if err := json.Unmarshal([]byte(ch.TargetCandidateJSON), &c); err != nil {
		return err
	}
	if c.Digest == "" {
		return fmt.Errorf("uplift change %d has no target candidate", changeID)
	}
	if err := a.Adoption.CheckTarget(ctx, p.ID, changeID, ch.TargetAdoptionID, ch.ExpectedRevision, c); err != nil {
		return err
	}
	_, err = a.Lifecycle.SettleUplift(adoption.WithSystemAuthority(ctx, p.ID), &p, &c, "apptown-system", fmt.Sprintf("change %d landed: %s", changeID, strings.Join(c.Words(), ", ")), changeID)
	return err
}

// Recover settles uplift changes that shipped without moving the project.
func (a UpliftSettlement) Recover(ctx context.Context) {
	changes, err := workflow.PendingUpliftAdoptionsIn(ctx, a.DB)
	if err != nil {
		log.Printf("reading pending uplift adoptions: %v", err)
		return
	}
	for _, change := range changes {
		if err := a.Complete(ctx, change.ID); err != nil {
			log.Printf("recovering uplift change %d: %v", change.ID, err)
		}
	}
}
