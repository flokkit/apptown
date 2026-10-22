package adoption

import (
	"context"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption/internal/adoptionsql"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/strs"
)

// adoptIn joins the caller's transaction. Work's completion marker remains the
// facade's participant; all adoption selections and receipts settle here.
func adoptIn(ctx context.Context, tx *database.Tx, current *Adoption, candidate Candidate, expected int64, who, reason string, attached func([]string) error, reviews ReviewRequests, at time.Time) (*Adoption, error) {
	projectID := current.ProjectID
	capabilities, err := requiredCapabilitiesIn(ctx, tx, candidate)
	if err != nil {
		return nil, err
	}
	candidate.RequiredCapabilities = capabilities
	plan, err := current.Prepare(candidate, attached)
	if err != nil {
		return nil, err
	}
	plan.reason = reason
	next, event, err := current.Advance(plan, expected)
	if err != nil {
		return nil, err
	}
	q := adoptionsql.New(tx.SQL())
	for _, policy := range plan.policies {
		n, err := q.PutProjectPolicy(ctx, adoptionsql.PutProjectPolicyParams{ID: ids.New(at.UTC()), ProjectID: projectID, Name: policy.name, Description: policy.description, Rule: policy.rule, Config: policy.config, Origin: policy.origin, At: at.UTC()})
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, fmt.Errorf("policy %q was not recorded", policy.name)
		}
	}
	for _, extension := range plan.extensions {
		n, err := q.EnableProjectExtension(ctx, adoptionsql.EnableProjectExtensionParams{ProjectID: projectID, Name: extension})
		if err != nil {
			return nil, err
		}
		if n == 0 {
			// Already enabled is a successful no-op; a suppressed new insert
			// must not be mistaken for that existing membership.
			if _, err := q.ProjectExtension(ctx, adoptionsql.ProjectExtensionParams{ProjectID: projectID, Name: extension}); err != nil {
				return nil, fmt.Errorf("extension %q was not enabled: %w", extension, err)
			}
		}
	}
	pins := make([]wow.ResolvedPin, 0, len(plan.receipt.Guidance))
	for _, block := range plan.receipt.Guidance {
		pins = append(pins, wow.ResolvedPin{BlockID: block.BlockID, Name: block.Name, Version: block.Version, Disabled: block.Disabled})
	}
	if err := PutImportsIn(ctx, tx, projectID, pins, at); err != nil {
		return nil, err
	}
	var lanes []string
	for _, workflow := range plan.receipt.Workflows {
		if !workflow.System {
			lanes = append(lanes, workflow.Name)
		}
	}
	if err := SelectOnlyIn(ctx, tx, projectID, lanes, at); err != nil {
		return nil, err
	}
	if err := SetMembershipIn(ctx, tx, projectID, plan.memberships, reason, who, at); err != nil {
		return nil, err
	}
	if err := saveAdvanceIn(ctx, tx, expected, next, plan, event, who, at, reviews); err != nil {
		return nil, err
	}
	return next, nil
}

// saveAdvanceIn records the revision, stamped at: the owner's clock, by
// which the ways of working it adopted are resolved as of that time. A
// recorded revision changes which checks the project answers to, so it
// requests that project's review in the same transaction (reviews nil:
// creation, which requests none); a failed request fails the revision.
func saveAdvanceIn(ctx context.Context, tx *database.Tx, expected int64, next *Adoption, plan Plan, event Event, who string, at time.Time, reviews ReviewRequests) error {
	q := adoptionsql.New(tx.SQL())
	if event.Recorded {
		receipt, err := strs.JSON(plan.receipt)
		if err != nil {
			return err
		}
		author, err := identity.ActorIn(ctx, tx, who)
		if err != nil {
			return err
		}
		if _, err := q.InsertAdoption(ctx, adoptionsql.InsertAdoptionParams{ID: ids.New(at.UTC()), ProjectID: next.ProjectID, Revision: next.Revision, Kind: event.Kind.String(), Reason: event.Reason, Digest: event.Digest, Actor: who, ActorPrincipal: author.Principal, ActorVia: author.Via, At: at.UTC(), Receipt: []byte(receipt)}); err != nil {
			return err
		}
	}
	// Even a reviewed no-op must take its checked write; a suppressed UPDATE
	// cannot report successful drift settlement.
	if _, err := q.SaveCurrentAdoption(ctx, adoptionsql.SaveCurrentAdoptionParams{ProjectID: next.ProjectID, ExpectedRevision: expected, Revision: next.Revision, Pack: next.Pack, Drift: next.Drift}); err != nil {
		return err
	}
	if !event.Recorded {
		return nil
	}
	payload := struct {
		Kind      AdoptionKind `json:"kind"`
		Reason    string       `json:"reason"`
		Digest    string       `json:"digest"`
		Guidance  int          `json:"guidance"`
		Workflows int          `json:"workflows"`
	}{event.Kind, event.Reason, event.Digest, event.Guidance, event.Workflows}
	if err := audit.At(at).RecordIn(ctx, tx, who, next.ProjectID, "adoption.recorded", fmt.Sprintf("r%d", next.Revision), payload); err != nil {
		return err
	}
	if reviews == nil {
		return nil
	}
	if err := reviews.RequestIn(ctx, tx, Recorded{projectID: next.ProjectID, revision: next.Revision, tx: tx}); err != nil {
		return fmt.Errorf("requesting the review of revision %d: %w", next.Revision, err)
	}
	return nil
}

// ReviewRequests admits a review of the project whose revision a
// transaction just recorded, inside that transaction, so the revision and
// the request commit together or not at all.
type ReviewRequests interface {
	RequestIn(ctx context.Context, tx *database.Tx, r Recorded) error
}

// Recorded is a revision this owner just recorded, built only here. It is
// the authority for the review it requests: Current says, in the recording
// transaction, whether the revision is still the project's current one.
type Recorded struct {
	projectID ids.ID
	revision  int64
	tx        *database.Tx
}

func (r Recorded) ProjectID() ids.ID { return r.projectID }
func (r Recorded) Revision() int64   { return r.revision }

// Current reads the project's current revision in the recording
// transaction. A zero Recorded (not built by this owner) is never current.
func (r Recorded) Current(ctx context.Context) (bool, error) {
	if r.tx == nil || r.projectID.IsZero() {
		return false, nil
	}
	current, err := CurrentForUpdateIn(ctx, r.tx, r.projectID)
	if err != nil {
		return false, err
	}
	return current.Revision == r.revision, nil
}
