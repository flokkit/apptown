package digitalocean

import (
	"context"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/digitalocean/dofake"
)

// What the forge asks of a managed cluster to protect its data: a new
// database is one the platform makes for the forge and hands to the app's
// user through the admin connection; a name the cluster already holds is
// refused, never adopted. The app user's connection is the platform's
// answer at use time. The platform's own backups come newest first. A
// removed database and a deleted cluster are gone from the platform's
// answers, and removing either twice is no error.
func TestManagedDataMakesNewPlacesAndNeverAdoptsOne(t *testing.T) {
	t.Parallel()
	fake := &dofake.API{}
	server := httptest.NewServer(fake.Handler())
	t.Cleanup(server.Close)
	fake.SeedDatabase(map[string]any{"id": "db-9", "name": "forge-demo-main", "engine": "pg", "status": "online",
		"connection": map[string]any{"uri": "postgresql://doadmin:secret@pg.example:25060/defaultdb", "host": "pg.example", "port": 25060}})
	client := NewClient("token", server.URL)
	var owned [][3]string
	m := ManagedData{
		Client: func(context.Context) (*Client, Config, error) { return client, Config{}, nil },
		OwnDatabase: func(_ context.Context, uri, db, user string) error {
			owned = append(owned, [3]string{uri, db, user})
			return nil
		},
	}
	ctx := context.Background()
	if err := client.Call(ctx, "POST", "/v2/databases/db-9/users", map[string]any{"name": "app"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.NewDatabase(ctx, "db-9", "app_restore_1", "app"); err != nil {
		t.Fatal(err)
	}
	if len(owned) != 1 || owned[0] != [3]string{"postgresql://doadmin:secret@pg.example:25060/defaultdb", "app_restore_1", "app"} {
		t.Fatalf("handed over: %v", owned)
	}
	if err := m.NewDatabase(ctx, "db-9", "app_restore_1", "app"); !errors.Is(err, ErrDatabaseExists) || len(owned) != 1 {
		t.Fatalf("a name the cluster holds was adopted: %v %v", err, owned)
	}
	conn, err := m.AppConnection(ctx, "db-9", "app_restore_1", "app")
	if err != nil || conn != (Connection{Host: "pg.example", Port: 25060, Database: "app_restore_1", User: "app", Password: "pw-app", SSLMode: "require"}) {
		t.Fatalf("connection: %+v %v", conn, err)
	}
	if _, err := m.AppConnection(ctx, "db-9", "app_restore_1", "nobody"); err == nil {
		t.Fatal("a user the cluster does not have gave a connection")
	}
	fake.SeedNativeBackups("db-9", "2026-10-06T06:10:00Z", "2026-10-07T06:10:00Z", "2026-10-01T06:10:00Z")
	backups, err := m.Backups(ctx, "db-9")
	if err != nil || len(backups) != 3 || backups[0].CreatedAt.Day() != 7 || backups[2].CreatedAt.Day() != 1 {
		t.Fatalf("backups: %+v %v", backups, err)
	}
	if err := m.DropDatabase(ctx, "db-9", "app_restore_1"); err != nil {
		t.Fatal(err)
	}
	if err := m.DropDatabase(ctx, "db-9", "app_restore_1"); err != nil {
		t.Fatalf("removing a removed database: %v", err)
	}
	if dbs, err := m.Databases(ctx, "db-9"); err != nil || slices.Contains(dbs, "app_restore_1") {
		t.Fatalf("databases: %v %v", dbs, err)
	}
	if err := m.DeleteInstance(ctx, "db-9"); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteInstance(ctx, "db-9"); err != nil {
		t.Fatalf("deleting a deleted cluster: %v", err)
	}
	if got := fake.Snapshot().DeletedClusters; len(got) != 1 || got[0] != "db-9" {
		t.Fatalf("deleted: %v", got)
	}
}

// DigitalOcean answers 422 both for a name the cluster holds and for one
// it does not accept (over 40 characters): only the first is "exists", and
// the other is a refusal in the platform's words, so a check's failure is
// never said to be a database that is already there.
func TestARefusedDatabaseNameIsNotOneThatExists(t *testing.T) {
	t.Parallel()
	fake := &dofake.API{}
	server := httptest.NewServer(fake.Handler())
	t.Cleanup(server.Close)
	fake.SeedDatabase(map[string]any{"id": "db-9", "name": "forge-demo-main", "engine": "pg", "status": "online",
		"connection": map[string]any{"uri": "postgresql://doadmin:secret@pg.example:25060/defaultdb", "host": "pg.example", "port": 25060}})
	client := NewClient("token", server.URL)
	m := ManagedData{Client: func(context.Context) (*Client, Config, error) { return client, Config{}, nil },
		OwnDatabase: func(context.Context, string, string, string) error { return nil }}
	err := m.NewDatabase(context.Background(), "db-9", "app_check_01a14807-9de9-7000-b5b7-3475953fa971", "app")
	if err == nil || errors.Is(err, ErrDatabaseExists) || !strings.Contains(err.Error(), "max 40 characters") || !deploy.Refused(err) {
		t.Fatalf("a name the platform does not accept: %v", err)
	}
}
