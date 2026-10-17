package deploy

import (
	"context"
	"flokkit.io/forge/internal/ids"
	"time"
)

// NeedRecorder is where a driver that provisions a need on its target
// records what it asked for and what it got, by the placement's id.
type NeedRecorder interface {
	RecordIntent(ctx context.Context, placementID ids.ID, need string, requested map[string]any) error
	RecordIdentity(ctx context.Context, placementID ids.ID, need string, requested map[string]any, id, name string) error
}

// ManagedData is what the forge asks of a provider's managed data instance
// (a database cluster) to protect the data in it: the app user's
// connection, read at use time and never recorded; a new, empty database,
// made by the provider and never adopted by name; its removal; the
// provider's own backups, read for recency only; and the instance's
// deletion. The instance's admin credential stays inside the provider
// module.
type ManagedData interface {
	AppConnection(ctx context.Context, instance, database, user string) (Connection, error)
	Databases(ctx context.Context, instance string) ([]string, error)
	NewDatabase(ctx context.Context, instance, name, owner string) error
	DropDatabase(ctx context.Context, instance, name string) error
	Backups(ctx context.Context, instance string) ([]NativeBackup, error)
	DeleteInstance(ctx context.Context, instance string) error
}

// BucketData is what the forge asks of a provider about a deployment's
// object storage bucket to protect its objects (design/subsystems/
// deployment.md, "Protecting data off-host"): the connection the
// deployment's own key for that bucket gives, read at use time and never
// recorded, and the teardown of the bucket and its key as a step of its
// own, so a take-down can stop the app, copy the bucket as the app's key,
// and only then delete it.
type BucketData interface {
	BucketConnection(ctx context.Context, bucket string) (Connection, error)
	TearDownStorage(ctx context.Context, p Placement, td TakeDown) TakenDown
}

// Connection is how a copy job reaches one database as the app's user.
// The password goes into the job's environment and nowhere else.
type Connection struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string
	SSLMode  string
}

// NativeBackup is one of the provider's own backups of an instance.
type NativeBackup struct {
	CreatedAt time.Time
	SizeGB    float64
}
