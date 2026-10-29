package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Transparent-read seeding is an owner persistence law: failed writes retry,
// revoked/expired choices remain choices, and another scope does not suppress
// the instance default.
func TestTransparentReadSeedFailureAndExistingChoices(t *testing.T) {
	t.Parallel()
	t.Run("failed seed can retry", func(t *testing.T) {
		db := testDB(t)
		counted := &countedQueries{Handle: db.SQL()}
		t.Cleanup(db.FailNextStatement("INSERT INTO grants"))
		if err := SeedTransparentReadsIn(context.Background(), counted, time.Now()); !errors.Is(err, database.ErrInjectedFailure) {
			t.Fatalf("lost bootstrap failure: %v", err)
		}
		wantLen(t, "grants", recordedGrants(t, db, "", ""), 0)
		if err := SeedTransparentReadsIn(context.Background(), counted, time.Now()); err != nil {
			t.Fatal(err)
		}
		if counted.reads != 0 || counted.writes != 1 {
			t.Fatalf("seed took %d reads and %d writes", counted.reads, counted.writes)
		}
		wantLen(t, "auditor grants", recordedGrants(t, db, GroupAllUsers, access.RoleAuditor), 1)
	})
	t.Run("expired choice survives", func(t *testing.T) {
		db := testDB(t)
		if err := SeedTransparentReadsIn(context.Background(), db, time.Now()); err != nil {
			t.Fatal(err)
		}
		recorded, err := queries(db).AllGrants(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, grant := range recorded {
			expireStoredGrant(t, db, grant.ID)
		}
		if err := SeedTransparentReadsIn(context.Background(), db, time.Now()); err != nil {
			t.Fatal(err)
		}
		wantLen(t, "grants", recordedGrants(t, db, "", ""), 1)
		got, err := ActiveGrantsIn(context.Background(), db, access.Principal{Kind: "user", Name: "reader"}, time.Now())
		if err != nil || len(got) != 0 {
			t.Fatalf("expired grant replaced or restored: %+v %v", got, err)
		}
	})
	t.Run("other scope does not suppress default", func(t *testing.T) {
		db := testDB(t)
		// An earlier release's owner recorded an Auditor grant at a project
		// when asked; this build's owner refuses a role away from its scope.
		// sql-guarantee: a revoked Auditor grant at a project, which no current owner operation can write.
		if _, err := db.Exec(`INSERT INTO grants (id, subject_type, subject, role_name, scope_type, scope_id, created_at, revoked_at) VALUES (?, 'group', ?, ?, 'project', ?, ?, ?)`,
			idstest.N(900), GroupAllUsers, access.RoleAuditor, idstest.N(1).String(), time.Now(), time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := SeedTransparentReadsIn(context.Background(), db, time.Now()); err != nil {
			t.Fatal(err)
		}
		instance := 0
		for _, g := range liveGrants(t, db, "", "") {
			if g.ScopeType == string(access.ScopeInstance) {
				instance++
			}
		}
		if instance != 1 {
			t.Fatalf("instance grants: %d", instance)
		}
	})
}
