package adoption

import (
	"context"
	"database/sql"
	"errors"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption/internal/adoptionsql"
)

// Adoption is the project's current selection. It is independently readable when
// a historical receipt is absent or corrupt; it never guesses from history.
type Adoption struct {
	recorded  *RecordedRevision
	ProjectID ids.ID `json:"projectId"`
	Revision  int64  `json:"revision"`
	Pack      string `json:"pack"`
	Drift     string `json:"drift"`
}

func currentRecord(r adoptionsql.Adoption) *Adoption {
	return &Adoption{ProjectID: r.ProjectID, Revision: r.Revision, Pack: r.Pack, Drift: r.DriftNote}
}

// CurrentIn reads only the aggregate. Absence is sql.ErrNoRows and is never repaired.
func CurrentIn(ctx context.Context, q database.Queryer, projectID ids.ID) (*Adoption, error) {
	r, err := adoptionsql.New(q.SQL()).CurrentAggregate(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return currentRecord(r), nil
}
func (s *Service) Current(ctx context.Context, projectID ids.ID) (*Adoption, error) {
	return CurrentIn(ctx, s.db, projectID)
}

// CurrentForUpdateIn locks project identity first, then the existing aggregate.
// Both must exist; reading for an update does not initialize missing state.
func CurrentForUpdateIn(ctx context.Context, tx *database.Tx, projectID ids.ID) (*Adoption, error) {
	q := adoptionsql.New(tx.SQL())
	if _, err := q.AdoptionProjectForUpdate(ctx, projectID); err != nil {
		return nil, err
	}
	r, err := q.CurrentAggregateForUpdate(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return currentRecord(r), nil
}

// InitializeIn is an explicit creation participant. Duplicate initialization is
// an error, and a missing project is never turned into orphaned adoption state.
func InitializeIn(ctx context.Context, tx *database.Tx, projectID ids.ID, revision int64, pack, drift string) error {
	q := adoptionsql.New(tx.SQL())
	if _, err := q.AdoptionProjectForUpdate(ctx, projectID); err != nil {
		return err
	}
	_, err := q.InsertCurrentAdoption(ctx, adoptionsql.InsertCurrentAdoptionParams{ProjectID: projectID, Revision: revision, Pack: pack, DriftNote: drift})
	return err
}

// ensureCurrentForUpdateIn is used only by explicit mutations. An unconfigured
// known project starts at revision zero under the same project lock.
func ensureCurrentForUpdateIn(ctx context.Context, tx *database.Tx, projectID ids.ID) (*Adoption, error) {
	q := adoptionsql.New(tx.SQL())
	if _, err := q.AdoptionProjectForUpdate(ctx, projectID); err != nil {
		return nil, err
	}
	r, err := q.CurrentAggregateForUpdate(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		r, err = q.InsertCurrentAdoption(ctx, adoptionsql.InsertCurrentAdoptionParams{ProjectID: projectID})
	}
	if err != nil {
		return nil, err
	}
	return currentRecord(r), nil
}

func SetPackIn(ctx context.Context, tx *database.Tx, projectID ids.ID, pack string) error {
	if _, err := ensureCurrentForUpdateIn(ctx, tx, projectID); err != nil {
		return err
	}
	_, err := adoptionsql.New(tx.SQL()).SetCurrentPack(ctx, adoptionsql.SetCurrentPackParams{ProjectID: projectID, Pack: pack})
	return err
}
func (s *Service) SetPack(ctx context.Context, projectID ids.ID, pack string) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error { return SetPackIn(ctx, tx, projectID, pack) })
}
func (s *Service) NoteDrift(ctx context.Context, projectID ids.ID, note string) error {
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		current, err := ensureCurrentForUpdateIn(ctx, tx, projectID)
		if err != nil {
			return err
		}
		event := current.NoteDrift(note)
		_, err = adoptionsql.New(tx.SQL()).SetCurrentDrift(ctx, adoptionsql.SetCurrentDriftParams{ProjectID: event.ProjectID, DriftNote: event.Drift})
		return err
	})
}
func (s *Service) ClearDrift(ctx context.Context, projectID ids.ID) error {
	return s.NoteDrift(ctx, projectID, "")
}
func (s *Service) ProjectsWithRevisions(ctx context.Context) ([]ids.ID, error) {
	return adoptionsql.New(s.db.SQL()).ProjectsWithRevisions(ctx)
}

// CurrentReceiptIDIn observes identity, without parsing or manufacturing a
// receipt. A known project without its exact recorded revision returns zero.
func CurrentReceiptIDIn(ctx context.Context, q database.Queryer, projectID ids.ID) (ids.ID, error) {
	return adoptionsql.New(q.SQL()).CurrentReceiptID(ctx, projectID)
}

// TargetOwnedIn checks the immutable uplift target identity, without interpreting its body.
func TargetOwnedIn(ctx context.Context, q database.Queryer, projectID, changeID, receiptID ids.ID, digest string) (bool, error) {
	return adoptionsql.New(q.SQL()).TargetOwned(ctx, adoptionsql.TargetOwnedParams{ReceiptID: receiptID, ProjectID: projectID, ChangeID: changeID, Kind: AdoptionTarget.String(), Digest: digest})
}
