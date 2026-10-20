package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
)

type NeedStore struct {
	db    *database.DB
	clock func() time.Time // AppTown's clock, which stamps a need's updates; nil is the real one
}

func (s NeedStore) now() time.Time { return clock.Or(s.clock)().UTC() }

// NewNeedStore builds the need records over db, stamped by now (nil is the
// real clock).
func NewNeedStore(db *database.DB, now func() time.Time) NeedStore {
	return NeedStore{db: db, clock: now}
}

// ErrIdentityHeld refuses a fulfilment that would replace a managed need's
// established identity with another: AppTown's copies take what the
// record names, and the app still runs on, or was filled into, the old one.
var ErrIdentityHeld = fault.New(fault.Conflict, "a managed need's cluster, database and user do not change in place")

// identityKeys name a managed need's data: the cluster (by the name the
// provider is asked for), the database in it and the user copies are taken
// as.
var identityKeys = []string{"name", "database", "user"}

// established says whether a recorded handle names a fulfilled identity:
// an instance, a database and a user.
func established(handle map[string]any) bool {
	for _, k := range []string{"id", "database", "user"} {
		if v, _ := handle[k].(string); v == "" {
			return false
		}
	}
	return true
}

// RecordIntent records what a fulfilment is about to ask the provider for,
// before it asks. A record whose identity is established is never
// replaced: asked for the same identity it is left as it is (a retry does
// not erase what was made), and asked for another it refuses with
// ErrIdentityHeld and writes nothing. Releases of one deployment run one at
// a time, so the read and the write in one transaction are enough.
func (s NeedStore) RecordIntent(ctx context.Context, deploymentID ids.ID, need string, requested map[string]any) error {
	now := s.now()
	_, err := database.Transact(ctx, s.db, func(tx *database.Tx) (struct{}, error) {
		q := deploymentsql.New(tx.SQL())
		rows, err := q.NeedInstances(ctx, []ids.ID{deploymentID})
		if err != nil {
			return struct{}{}, err
		}
		for _, r := range rows {
			if r.Need != need {
				continue
			}
			held := jsonMap(string(r.HandleJson))
			if !established(held) {
				break
			}
			var differ []string
			for _, k := range identityKeys {
				has, _ := held[k].(string)
				want, _ := requested[k].(string)
				if has != want {
					differ = append(differ, fmt.Sprintf("%s %s, not %s", k, has, want))
				}
			}
			if len(differ) > 0 {
				return struct{}{}, fmt.Errorf("%w: %s keeps its data at %s; this release asks for another, so nothing was made. Set it back, or start a new deployment from a copy", ErrIdentityHeld, need, strings.Join(differ, ", "))
			}
			return struct{}{}, nil // the established identity, asked again: kept as it is
		}
		_, err = q.RecordNeedIntent(ctx, deploymentsql.RecordNeedIntentParams{ID: ids.New(now),
			DeploymentID: deploymentID,
			Need:         need,
			HandleJson:   json.RawMessage(jsonText(map[string]any{"requested": requested})),
			Status:       NeedCreating.String(),
			UpdatedAt:    now,
			ReadyState:   NeedReady.String(),
		})
		return struct{}{}, err
	})
	return err
}

func (s NeedStore) RecordIdentity(ctx context.Context, deploymentID ids.ID, need string, requested map[string]any, id, name string) error {
	ctx = context.WithoutCancel(ctx) // the provider made it: its identity is recorded though ctx has ended
	result, err := deploymentsql.New(s.db.SQL()).RecordNeedIdentity(ctx, deploymentsql.RecordNeedIdentityParams{
		HandleJson:   json.RawMessage(jsonText(map[string]any{"requested": requested, "id": id, "name": name})),
		DeploymentID: deploymentID,
		Need:         need,
	})
	if err != nil {
		return err
	}
	n := result
	if n != 1 {
		return fmt.Errorf("managed need %s on deployment %d has no intent record", need, deploymentID)
	}
	return nil
}
