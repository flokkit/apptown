package identity

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
)

// The instance setting naming AppTown's last word on its own Docker:
// "used", the last admitted start could run project work on it; "none", the
// last admitted start was in the -no-local-docker mode; "drained", an AppTown
// with its Docker witnessed that no local work or execution container was
// left (apptownd -drain-local-docker). Unset: a database no run was ever
// recorded in (migration 102 marks every earlier one "used").
const (
	SettingLocalDocker = "local_docker"
	LocalDockerUsed    = "used"
	LocalDockerNone    = "none"
	LocalDockerDrained = "drained"
)

// RecordLocalDockerIn records the Docker mode of a start AppTown has
// admitted, before it runs any work.
func RecordLocalDockerIn(ctx context.Context, q database.Queryer, used bool) error {
	value := LocalDockerNone
	if used {
		value = LocalDockerUsed
	}
	return putLocalDocker(ctx, q, value)
}

// RecordLocalDockerDrainedIn records a witnessed drain of AppTown's own
// Docker. Only the drain, having observed that Docker, may call it.
func RecordLocalDockerDrainedIn(ctx context.Context, q database.Queryer) error {
	return putLocalDocker(ctx, q, LocalDockerDrained)
}

func putLocalDocker(ctx context.Context, q database.Queryer, value string) error {
	_, err := queries(q).PutInstanceSetting(ctx, accesssql.PutInstanceSettingParams{Key: SettingLocalDocker, Value: value})
	return err
}
