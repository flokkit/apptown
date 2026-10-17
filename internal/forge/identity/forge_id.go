package identity

import (
	"context"
	"fmt"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/identity/internal/accesssql"
	"flokkit.io/forge/internal/ids"
)

// SettingForgeID is the instance setting holding the forge's own UUID: what
// names this forge in exports, backups and the former ids its records kept.
// It is written once and never changed; the instance-id file stays the
// telemetry name it was.
const SettingForgeID = "forge.id"

// ForgeIDIn is the forge's UUID, minted from at the first time it is asked
// for. Concurrent first asks agree: the first insert wins and every caller
// reads it back.
func ForgeIDIn(ctx context.Context, q database.Queryer, at time.Time) (ids.ID, error) {
	if err := queries(q).InsertInstanceSettingOnce(ctx, accesssql.InsertInstanceSettingOnceParams{Key: SettingForgeID, Value: ids.New(at).String()}); err != nil {
		return ids.Nil, err
	}
	value, err := InstanceSettingIn(ctx, q, SettingForgeID)
	if err != nil {
		return ids.Nil, err
	}
	id, err := ids.Parse(value)
	if err != nil || id.IsZero() {
		return ids.Nil, fmt.Errorf("the forge's id %q is not a UUID", value)
	}
	return id, nil
}

// ForgeID is the forge's UUID (ForgeIDIn) on the service's clock.
func (s *Service) ForgeID(ctx context.Context) (ids.ID, error) {
	return ForgeIDIn(ctx, s.db, s.now())
}
