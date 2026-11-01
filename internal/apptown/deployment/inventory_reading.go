package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

// InventoryReadings owns the last complete provider inventory and the claims it
// checked. A later failure or restart cannot erase a witnessed discrepancy.
type InventoryReadings struct {
	db  *database.DB
	now func() time.Time
}

func NewInventoryReadings(db *database.DB, now func() time.Time) *InventoryReadings {
	return &InventoryReadings{db: db, now: clock.Or(now)}
}
func inventoryIdentityFor(t *Target) string { return fmt.Sprintf("%s/%d", t.ID, t.SettingsRevision) }

type inventoryObservation struct {
	LastFailedAt                      time.Time
	LastFailure                       string
	Items                             []deploy.Resource
	Shared                            []SharedResource
	Missing                           []deploy.Resource
	ObservedAt, AttemptedAt, FailedAt time.Time
	Reason                            string
	Unknown, Stale, Invalidated       bool
}

func (c *InventoryReadings) Start(ctx context.Context, t *Target) (int64, error) {
	r, err := deploymentsql.New(c.db.SQL()).StartInventoryReading(ctx, deploymentsql.StartInventoryReadingParams{ID: ids.New(c.now()), Subject: t.Name, Kind: "inventory", Identity: inventoryIdentityFor(t)})
	return r.Generation, err
}
func (c *InventoryReadings) Invalidate(ctx context.Context, name string) error {
	// Initializing a row makes an act before the first reading observable too.
	t, err := NewTargets(c.db, nil).ByName(ctx, name)
	if err != nil {
		return err
	}
	if _, err = c.Start(ctx, t); err != nil {
		return err
	}
	return deploymentsql.New(c.db.SQL()).InvalidateReading(ctx, deploymentsql.InvalidateReadingParams{Subject: name, Kind: "inventory"})
}
func (c *InventoryReadings) SnapshotForTarget(ctx context.Context, t *Target) (inventoryObservation, error) {
	row, err := deploymentsql.New(c.db.SQL()).Reading(ctx, deploymentsql.ReadingParams{Subject: t.Name, Kind: "inventory"})
	if errors.Is(err, sql.ErrNoRows) || err == nil && row.Identity != inventoryIdentityFor(t) {
		return inventoryObservation{Unknown: true, Stale: true}, nil
	}
	if err != nil {
		return inventoryObservation{Unknown: true, Stale: true}, err
	}
	var out inventoryObservation
	if err = json.Unmarshal(row.Payload, &out); err != nil {
		return out, err
	}
	out.Invalidated = row.Invalidated
	out.Stale = out.ObservedAt.IsZero() || c.now().Sub(out.ObservedAt) > liveFresh
	out.Unknown = out.ObservedAt.IsZero() || c.now().Sub(out.ObservedAt) > liveUnknown
	return out, nil
}
func (c *InventoryReadings) Finish(ctx context.Context, t *Target, generation int64, items []deploy.Resource, shared []SharedResource, recorded []deploy.Resource, checkErr error) error {
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	old, err := c.SnapshotForTarget(finish, t)
	if err != nil {
		return errors.Join(checkErr, err)
	}
	now := c.now()
	old.AttemptedAt = now
	if checkErr != nil {
		old.FailedAt = now
		old.LastFailedAt = now
		old.Reason = "The inventory check failed."
		if errors.Is(checkErr, context.DeadlineExceeded) {
			old.Reason = "The inventory check timed out."
		}
		old.LastFailure = old.Reason
	} else {
		lastAt, lastFailure := old.LastFailedAt, old.LastFailure
		old = inventoryObservation{LastFailedAt: lastAt, LastFailure: lastFailure, Items: items, Shared: shared, ObservedAt: now, AttemptedAt: now}
		for _, r := range MergeInventory(recorded, items) {
			if r.Record == deploy.RecordMissing {
				old.Missing = append(old.Missing, r)
			}
		}
	}
	raw, err := json.Marshal(old)
	if err != nil {
		return errors.Join(checkErr, err)
	}
	_, err = deploymentsql.New(c.db.SQL()).FinishInventoryReading(finish, deploymentsql.FinishInventoryReadingParams{Subject: t.Name, Kind: "inventory", Identity: inventoryIdentityFor(t), Generation: generation, Payload: raw, Invalidated: checkErr != nil && old.Invalidated})
	return errors.Join(checkErr, err)
}

// Live is the passive infrastructure view; old evidence retains its original time.
// The final flags are invalidated and unknown, classified by this owner.
func (c *InventoryReadings) Live(ctx context.Context, t *Target) ([]deploy.Resource, []SharedResource, string, time.Time, bool, bool) {
	out, err := c.SnapshotForTarget(ctx, t)
	if err != nil {
		return nil, nil, err.Error(), time.Time{}, false, true
	}
	why := out.Reason
	if why == "" && (out.Stale || out.Invalidated) {
		why = "Inventory reading is out of date"
	}
	if out.Unknown {
		return nil, nil, why, out.ObservedAt, out.Invalidated, out.Unknown
	}
	return out.Items, out.Shared, why, out.ObservedAt, out.Invalidated, out.Unknown
}
