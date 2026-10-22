package deployment

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/enum"
)

// A reconciliation is one pass that brings a deployment's managed needs to
// what the contract declares (§4.1), recorded as it runs with a receipt of
// what the provider answered.
type ReconciliationState uint8

const (
	ReconciliationRunning ReconciliationState = iota + 1
	ReconciliationDone
	ReconciliationFailed
)

// reconciliationStateCodec spells each ReconciliationState in storage and JSON.
var reconciliationStateCodec = enum.Set[ReconciliationState]{
	Names:     []string{"running", "done", "failed"},
	Invalid:   "invalid-reconciliation-state",
	Err:       ErrReconciliationState,
	ScanLabel: "value",
}

var (
	ErrReconciliationState      = errors.New("invalid reconciliation state")
	ErrReconciliationTransition = errors.New("invalid reconciliation transition")
)

func (s ReconciliationState) String() string { return reconciliationStateCodec.String(s) }

// Ended is the one rule: a running pass ends done or failed, once.
func (s ReconciliationState) Ended(failed bool) (ReconciliationState, error) {
	if s != ReconciliationRunning {
		return 0, fmt.Errorf("%w: pass is already %s", ErrReconciliationTransition, s)
	}
	if failed {
		return ReconciliationFailed, nil
	}
	return ReconciliationDone, nil
}

func (s ReconciliationState) Value() (driver.Value, error) { return reconciliationStateCodec.Value(s) }

func (s *ReconciliationState) Scan(value any) error { return reconciliationStateCodec.Scan(s, value) }

func (s ReconciliationState) MarshalJSON() ([]byte, error) {
	return reconciliationStateCodec.JSON(s)
}

type Reconciliations struct {
	db    *database.DB
	clock func() time.Time // AppTown's clock, which stamps a pass; nil is the real one
}

func (s *Reconciliations) now() time.Time { return clock.Or(s.clock)().UTC() }

func NewReconciliations(db *database.DB) *Reconciliations { return &Reconciliations{db: db} }

// Begin records a running pass and returns its id.
func (s *Reconciliations) Begin(ctx context.Context, deploymentID ids.ID, who string) (ids.ID, error) {
	now := s.now()
	starter, err := identity.ActorIn(ctx, s.db, who)
	if err != nil {
		return ids.Nil, err
	}
	r, err := deploymentsql.New(s.db.SQL()).InsertReconciliation(ctx, deploymentsql.InsertReconciliationParams{ID: ids.New(now), DeploymentID: deploymentID, Status: ReconciliationRunning.String(), Actor: who, ActorPrincipal: starter.Principal, ActorVia: starter.Via, StartedAt: now})
	return r.ID, err
}

// End records the pass's outcome and receipt; a pass ends once.
func (s *Reconciliations) End(ctx context.Context, id ids.ID, failed bool, receipt map[string]any) error {
	ctx = context.WithoutCancel(ctx) // the reconciliation ran: its end is recorded though ctx has ended
	now := s.now()
	next, err := ReconciliationRunning.Ended(failed)
	if err != nil {
		return err
	}
	res, err := deploymentsql.New(s.db.SQL()).EndReconciliation(ctx, deploymentsql.EndReconciliationParams{
		Status:       next.String(),
		ReceiptJson:  json.RawMessage(jsonText(receipt)),
		FinishedAt:   sql.NullTime{Time: now, Valid: true},
		ID:           id,
		RunningState: ReconciliationRunning.String(),
	})
	if err != nil {
		return err
	}
	if res != 1 {
		return fmt.Errorf("%w: reconciliation %d is not running", ErrReconciliationTransition, id)
	}
	return nil
}
