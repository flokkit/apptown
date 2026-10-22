package adoption

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption/internal/adoptionsql"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/database"
)

// Target is the scoped Change admission whose immutable procedure Adoption owns.
type Target struct {
	ProjectID, ChangeID ids.ID
	Candidate           Candidate
	Actor               string
}

type ScopedTarget struct {
	ID        ids.ID
	Candidate json.RawMessage
}

// recordTargetIn joins the already-authorized Work admission transaction. A
// target is immutable history at the negative Change identity, never current.
func recordTargetIn(ctx context.Context, tx *database.Tx, in Target, at time.Time) (ScopedTarget, error) {
	if in.ProjectID.IsZero() || in.ChangeID.IsZero() || in.Candidate.ProjectID != in.ProjectID || strings.TrimSpace(in.Actor) == "" {
		return ScopedTarget{}, fmt.Errorf("invalid adoption target scope")
	}
	digest := Digest(in.Candidate.Receipt)
	if in.Candidate.Digest == "" || in.Candidate.Digest != digest {
		return ScopedTarget{}, fmt.Errorf("target candidate digest does not match its receipt")
	}
	encoded, err := json.Marshal(in.Candidate)
	if err != nil {
		return ScopedTarget{}, err
	}
	receipt, err := json.Marshal(in.Candidate.Receipt)
	if err != nil {
		return ScopedTarget{}, err
	}
	author, err := identity.ActorIn(ctx, tx, in.Actor)
	if err != nil {
		return ScopedTarget{}, err
	}
	record, err := adoptionsql.New(tx.SQL()).InsertAdoption(ctx, adoptionsql.InsertAdoptionParams{ID: ids.New(at), ProjectID: in.ProjectID, ChangeID: in.ChangeID, Kind: AdoptionTarget.String(), Reason: fmt.Sprintf("the target of change %s", in.ChangeID), Digest: digest, Actor: in.Actor, ActorPrincipal: author.Principal, ActorVia: author.Via, At: at.UTC(), Receipt: receipt})
	if err != nil {
		return ScopedTarget{}, err
	}
	return ScopedTarget{ID: record.ID, Candidate: encoded}, nil
}

// CheckTarget binds a stored Change candidate to its immutable Adoption receipt.
// It does not substitute current state or an unrelated matching digest.
func (s *Service) CheckTarget(ctx context.Context, projectID ids.ID, changeID ids.ID, receiptID ids.ID, expectedRevision int64, c Candidate) error {
	recorded, err := ProjectAdoptionIn(ctx, s.db, projectID, receiptID)
	if err != nil {
		return err
	}
	return recorded.checkTarget(projectID, changeID, expectedRevision, c)
}
func (r RecordedRevision) checkTarget(projectID ids.ID, changeID ids.ID, expectedRevision int64, c Candidate) error {
	if projectID.IsZero() || changeID.IsZero() || r.ProjectID != projectID || r.ChangeID != changeID || r.Kind != AdoptionTarget || c.ExpectedRevision != expectedRevision {
		return fmt.Errorf("candidate is not the recorded target of this Change")
	}
	if c.Digest == "" || c.Digest != Digest(c.Receipt) || r.Digest != Digest(r.Receipt) || r.Digest != c.Digest {
		return fmt.Errorf("candidate differs from this Change's recorded adoption target")
	}
	return nil
}
