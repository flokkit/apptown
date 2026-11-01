package identity

import (
	"context"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// settingAppTownID is the instance setting holding AppTown's own UUID: what
// names this AppTown in exports, backups and the former ids its records kept.
// It is written once and never changed; the instance-id file stays the
// telemetry name it was.
const settingAppTownID = "forge.id"

// AppTownIDIn is AppTown's UUID, minted from at the first time it is asked
// for. Concurrent first asks agree: the first insert wins and every caller
// reads it back.
func AppTownIDIn(ctx context.Context, q database.Queryer, at time.Time) (ids.ID, error) {
	if err := queries(q).InsertInstanceSettingOnce(ctx, accesssql.InsertInstanceSettingOnceParams{Key: settingAppTownID, Value: ids.New(at).String()}); err != nil {
		return ids.Nil, err
	}
	value, err := InstanceSettingIn(ctx, q, settingAppTownID)
	if err != nil {
		return ids.Nil, err
	}
	id, err := ids.Parse(value)
	if err != nil || id.IsZero() {
		return ids.Nil, fmt.Errorf("AppTown's id %q is not a UUID", value)
	}
	return id, nil
}

// AppTownID is AppTown's UUID (AppTownIDIn) on the service's clock.
func (s *Service) AppTownID(ctx context.Context) (ids.ID, error) {
	return AppTownIDIn(ctx, s.db, s.now())
}
