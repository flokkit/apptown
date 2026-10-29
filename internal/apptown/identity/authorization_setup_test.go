package identity

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
)

func identityCount(t *testing.T, db *database.DB, want int, query string, args ...any) {
	t.Helper()
	var got int
	if err := db.SQL().QueryRowContext(context.Background(), query, args...).Scan(&got); err != nil || got != want {
		t.Fatalf("count %d want %d: %v", got, want, err)
	}
}

func identityRace(n int, fn func(int) int) []int {
	results := make([]int, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			results[i] = fn(i)
		}(i)
	}
	wg.Wait()
	return results
}

func identityResultCount(results []int, want int) int {
	got := 0
	for _, result := range results {
		if result == want {
			got++
		}
	}
	return got
}

// vetoIdentityWrite is a private persistence fixture for atomicity
// tests. It deliberately suppresses one owner's write participant.
func vetoIdentityWrite(t *testing.T, db *database.DB, table, operation, body string) func() {
	t.Helper()
	if _, err := db.SQL().ExecContext(context.Background(), `CREATE FUNCTION veto_identity_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(context.Background(), `CREATE TRIGGER veto_identity_write BEFORE `+operation+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION veto_identity_write()`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := db.SQL().ExecContext(context.Background(), `DROP TRIGGER veto_identity_write ON `+table); err != nil {
			t.Fatal(err)
		}
		if _, err := db.SQL().ExecContext(context.Background(), `DROP FUNCTION veto_identity_write()`); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPersonalGroupCreationIsAtomic(t *testing.T) {
	t.Parallel()
	for _, table := range []string{"groups", "group_members", "grants"} {
		t.Run(table, func(t *testing.T) {
			s := New(testDB(t), Config{})
			testGrantRoot(t, s, "boss")
			drop := vetoIdentityWrite(t, s.db, table, "INSERT", "RETURN NULL;")
			if _, err := s.PersonalGroup(context.Background(), "alice", "boss"); err == nil {
				t.Fatal("accepted missing ownership record")
			}
			if groupRecorded(t, s.db, "alice") {
				t.Fatal("rolled-back personal group was recorded")
			}
			wantLen(t, "memberships", memberships(t, s.db, "alice"), 0)
			wantLen(t, "grants", recordedGrants(t, s.db, "alice", ""), 0)
			drop()
			if name, err := s.PersonalGroup(context.Background(), "alice", "boss"); err != nil || name != "alice" {
				t.Fatalf("retry: %s %v", name, err)
			}
			wantLen(t, "memberships", memberships(t, s.db, "alice"), 1)
			wantLen(t, "owner grants", recordedGrants(t, s.db, "alice", access.RoleGroupOwner), 1)
		})
	}
}

func TestBootstrapAdministratorRollsBackAndHasOneWinner(t *testing.T) {
	t.Parallel()
	for _, table := range []string{"users", "groups", "group_members", "grants", "audit_log", "sessions"} {
		t.Run(table, func(t *testing.T) {
			s := New(testDB(t), Config{})
			drop := vetoIdentityWrite(t, s.db, table, "INSERT", "RETURN NULL;")
			if _, err := s.Bootstrap(context.Background(), NewAccount{Username: "alice", Password: "test-password"}); err == nil {
				t.Fatal("accepted incomplete first administrator")
			}
			if userRecorded(t, s.db, "alice") || groupRecorded(t, s.db, "alice") {
				t.Fatal("rolled-back first administrator left an account or group")
			}
			wantLen(t, "grants", recordedGrants(t, s.db, "alice", ""), 0)
			drop()
			if _, err := s.Bootstrap(context.Background(), NewAccount{Username: "alice", Password: "test-password"}); err != nil {
				t.Fatal(err)
			}
		})
	}
	s := New(testDB(t), Config{})
	results := identityRace(6, func(i int) int {
		_, err := s.Bootstrap(context.Background(), NewAccount{Username: fmt.Sprintf("first-%d", i), Password: "test-password"})
		if err == nil {
			return 1
		}
		if !errors.Is(err, ErrAlreadyBootstrapped) {
			t.Error(err)
		}
		return 0
	})
	if identityResultCount(results, 1) != 1 {
		t.Fatalf("bootstrap winners: %v", results)
	}
	users, err := s.Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	roots := 0
	for _, user := range users {
		if user.Role == "admin" {
			roots++
		}
	}
	if roots != 1 {
		t.Fatalf("root accounts: %d", roots)
	}
	wantLen(t, "bootstrap receipts", ledger(t, s.db, "auth.bootstrap"), 1)
	// sql-guarantee: The losing bootstraps never hand back a token, so no owner read can name their sessions; the table total proves none was left behind.
	identityCount(t, s.db, 1, `SELECT COUNT(*) FROM sessions`)
}

func TestConcurrentPersonalGroupAndGrantCreationAreIdempotent(t *testing.T) {
	t.Parallel()
	s := New(testDB(t), Config{})
	testAccounts(t, s.db, "alice", "boss")
	results := identityRace(8, func(int) int {
		name, err := s.PersonalGroup(context.Background(), "alice", "boss")
		if err != nil || name != "alice" {
			t.Errorf("personal group: %s %v", name, err)
		}
		if id, err := s.Grant(context.Background(), Spec{SubjectType: "user", Subject: "alice", Role: access.RoleProjectCreator, Scope: access.ScopeInstance, By: "boss"}, ""); err != nil || id.IsZero() {
			t.Errorf("grant failed: %d %v", id, err)
		}
		return 0
	})
	if identityResultCount(results, 0) != 8 {
		t.Fatalf("concurrent operations: %v", results)
	}
	if !groupRecorded(t, s.db, "alice") {
		t.Fatal("personal group missing")
	}
	wantLen(t, "memberships", memberships(t, s.db, "alice"), 1)
	wantLen(t, "owner grants", recordedGrants(t, s.db, "alice", access.RoleGroupOwner), 1)
	wantLen(t, "creator grants", recordedGrants(t, s.db, "alice", access.RoleProjectCreator), 1)
}

func TestAdministratorRevocationAndSavingAreAtomic(t *testing.T) {
	t.Parallel()
	for _, table := range []string{"grants", "audit_log"} {
		t.Run(table, func(t *testing.T) {
			s := New(testDB(t), Config{})
			if err := s.SeedAdministrator(context.Background(), "boss"); err != nil {
				t.Fatal(err)
			}
			if err := s.SeedAdministrator(context.Background(), "second"); err != nil {
				t.Fatal(err)
			}
			admin := liveGrants(t, s.db, "boss", access.RoleInstanceAdmin)
			wantLen(t, "administrator grants", admin, 1)
			id := admin[0].ID
			op := "UPDATE"
			if table == "audit_log" {
				op = "INSERT"
			}
			drop := vetoIdentityWrite(t, s.db, table, op, "RETURN NULL;")
			err := s.db.InTx(context.Background(), func(tx *database.Tx) error {
				_, err := RevokeIn(context.Background(), tx, id, "second", time.Now())
				return err
			})
			if err == nil {
				t.Fatal("accepted incomplete revocation")
			}
			if role, _, err := RoleIn(context.Background(), s.db, "boss", time.Now()); err != nil || role != "admin" {
				t.Fatalf("role: %s %v", role, err)
			}
			if _, _, found, err := s.LiveGrant(context.Background(), id); err != nil || !found {
				t.Fatalf("failed revocation lost the grant: %v %v", found, err)
			}
			drop()
			if err := s.SaveUser(context.Background(), AccountChange{Username: "boss", Role: "member"}, admittedPrincipal(t, s, "second")); err != nil {
				t.Fatal(err)
			}
			if err := s.EnsureDefaults(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			if role, _, err := RoleIn(context.Background(), s.db, "boss", time.Now()); err != nil || role != "member" {
				t.Fatalf("role after save: %s %v", role, err)
			}
			wantLen(t, "administrator grants", liveGrants(t, s.db, "boss", access.RoleInstanceAdmin), 0)
		})
	}
	s := New(testDB(t), Config{})
	if err := s.SeedAdministrator(context.Background(), "boss"); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedAdministrator(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	results := identityRace(2, func(i int) int {
		err := s.SaveUser(context.Background(), AccountChange{Username: []string{"boss", "second"}[i], Role: "member"}, admittedPrincipal(t, s, []string{"boss", "second"}[i]))
		if err == nil {
			return 1
		}
		if !errors.Is(err, ErrLastAdministrator) {
			t.Error(err)
		}
		return 0
	})
	if identityResultCount(results, 1) != 1 {
		t.Fatalf("last administrator race: %v", results)
	}
	admins := 0
	users, err := s.Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		if user.Role == "admin" {
			admins++
		}
	}
	if admins != 1 {
		t.Fatalf("root accounts: %d", admins)
	}
	wantLen(t, "administrator grants", liveGrants(t, s.db, "", access.RoleInstanceAdmin), 1)
}

func TestUserSaveRequiresPromotionAndSessionRemoval(t *testing.T) {
	t.Parallel()
	for _, table := range []string{"grants", "sessions", "audit_log"} {
		t.Run(table, func(t *testing.T) {
			s := New(testDB(t), Config{})
			if err := s.SeedAdministrator(context.Background(), "boss"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.EnsureUser(context.Background(), "kid"); err != nil {
				t.Fatal(err)
			}
			if err := s.SaveUser(context.Background(), AccountChange{Username: "kid", Password: "initial-password"}, admittedPrincipal(t, s, "boss")); err != nil {
				t.Fatal(err)
			}
			var signedIn []string
			for range 2 {
				in, err := s.SignIn(context.Background(), PasswordLogin{Username: "kid", Password: "initial-password"})
				if err != nil {
					t.Fatal(err)
				}
				signedIn = append(signedIn, in.Token)
			}
			before, err := queries(s.db).UserByName(context.Background(), "kid")
			if err != nil {
				t.Fatal(err)
			}
			keptSession := HashToken(signedIn[0])
			op, body := "INSERT", "RETURN NULL;"
			if table == "sessions" {
				op, body = "DELETE", fmt.Sprintf("IF OLD.token_hash='%s' THEN RETURN NULL; END IF; RETURN OLD;", keptSession)
			}
			drop := vetoIdentityWrite(t, s.db, table, op, body)
			edit := AccountChange{Username: "kid", Role: "admin", Password: "new-test-password"}
			if err := s.SaveUser(context.Background(), edit, admittedPrincipal(t, s, "boss")); err == nil {
				t.Fatal("accepted incomplete user change")
			}
			if after, err := queries(s.db).UserByName(context.Background(), "kid"); err != nil || after.PasswordHash != before.PasswordHash {
				t.Fatalf("failed save changed password hash: before=%q after=%q err=%v", before.PasswordHash, after.PasswordHash, err)
			}
			for _, token := range signedIn {
				if !sessionStored(t, s.db, token) {
					t.Fatal("failed save ended a session")
				}
			}
			// sql-guarantee: No owner read lists an account's sessions; the account's total must stay exactly two, not merely keep the two known tokens.
			identityCount(t, s.db, 2, `SELECT COUNT(*) FROM sessions_named WHERE username='kid'`)
			drop()
			if err := s.SaveUser(context.Background(), edit, admittedPrincipal(t, s, "boss")); err != nil {
				t.Fatal(err)
			}
			for _, token := range signedIn {
				if sessionStored(t, s.db, token) {
					t.Fatal("saved password change kept a session")
				}
			}
			// sql-guarantee: No owner read lists an account's sessions; a password change must leave the account none at all, not merely end the two known tokens.
			identityCount(t, s.db, 0, `SELECT COUNT(*) FROM sessions_named WHERE username='kid'`)
			wantLen(t, "administrator grants", liveGrants(t, s.db, "kid", access.RoleInstanceAdmin), 1)
		})
	}
}

// A recorded personal-group choice is never repaired, even when its member
// and owner grant rows are revoked and expired; retired names remain choices.
func personalGroupExec(t *testing.T, db *database.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.SQL().ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestPersonalGroupPreservesRecordedChoices(t *testing.T) {
	t.Parallel()
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			s := New(testDB(t), Config{})
			if _, err := s.EnsureUser(context.Background(), "alice"); err != nil {
				t.Fatal(err)
			}
			if fallback {
				storeOrdinaryGroup(t, s.db, "alice")
			}
			name, err := s.PersonalGroup(context.Background(), "alice", "boss")
			if err != nil {
				t.Fatal(err)
			}
			// sql-guarantee: Keep the same membership both revoked and expired so personal-group lookup cannot restore it; named membership mutations insert live rows or delete them, losing this historical choice.
			personalGroupExec(t, s.db, `UPDATE group_members SET revoked_at=now(),expires_at=now()-interval '1 day' WHERE principal_id=$1`, principalOf(t, s.db, "alice"))
			recorded, err := queries(s.db).AllGrants(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, grant := range recorded {
				if grant.Subject == "alice" {
					revokeExpiredStoredGrant(t, s.db, grant)
				}
			}
			identityRace(8, func(int) int {
				chosen, err := s.PersonalGroup(context.Background(), "alice", "boss")
				if err != nil || chosen != name {
					t.Errorf("existing choice: %s %v", chosen, err)
				}
				return 0
			})
			kept := memberships(t, s.db, "alice")
			if len(kept) != 1 || !kept[0].RevokedAt.Valid || !kept[0].ExpiresAt.Valid || !kept[0].ExpiresAt.Time.Before(time.Now()) {
				t.Fatalf("recorded membership was repaired: %+v", kept)
			}
			if kept := recordedGrants(t, s.db, "alice", ""); len(kept) != 1 || !kept[0].RevokedAt.Valid || !kept[0].ExpiresAt.Valid || !kept[0].ExpiresAt.Time.Before(time.Now()) {
				t.Fatalf("recorded grant was repaired or added to: %+v", kept)
			}
			for _, grant := range recorded {
				if grant.Subject != "alice" {
					continue
				}
				if stored, err := queries(s.db).GrantByID(context.Background(), grant.ID); err != nil || !stored.RevokedAt.Valid || !stored.ExpiresAt.Time.Before(time.Now()) {
					t.Fatalf("recorded grant was repaired: %+v %v", stored, err)
				}
			}
			// sql-guarantee: Retire the recorded personal group without deleting its identity; SaveGroup cannot set retired_at and DeleteGroup would test a missing choice instead.
			personalGroupExec(t, s.db, `UPDATE groups SET retired_at=now() WHERE name=$1`, name)
			if chosen, err := s.PersonalGroup(context.Background(), "alice", "boss"); err != nil || chosen != name {
				t.Fatalf("retired choice: %s %v", chosen, err)
			}
		})
	}
	t.Run("both ordinary names occupied", func(t *testing.T) {
		s := New(testDB(t), Config{})
		if _, err := s.EnsureUser(context.Background(), "alice"); err != nil {
			t.Fatal(err)
		}
		storeOrdinaryGroup(t, s.db, "alice")
		storeOrdinaryGroup(t, s.db, "alice-personal")
		if _, err := s.PersonalGroup(context.Background(), "alice", "alice"); err == nil {
			t.Fatal("claimed ordinary group")
		}
		wantLen(t, "grants", recordedGrants(t, s.db, "alice", ""), 0)
		wantLen(t, "memberships", memberships(t, s.db, "alice"), 0)
	})
	t.Run("another person's suffix", func(t *testing.T) {
		s := New(testDB(t), Config{})
		for _, user := range []string{"alice", "alice-personal"} {
			if _, err := s.EnsureUser(context.Background(), user); err != nil {
				t.Fatal(err)
			}
		}
		storeOrdinaryGroup(t, s.db, "alice")
		if _, err := s.PersonalGroup(context.Background(), "alice", "boss"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PersonalGroup(context.Background(), "alice-personal", "boss"); err == nil {
			t.Fatal("claimed another person's personal group")
		}
		wantLen(t, "grants", recordedGrants(t, s.db, "alice-personal", ""), 0)
	})
	t.Run("read failure", func(t *testing.T) {
		s := New(testDB(t), Config{})
		t.Cleanup(s.db.FailNextStatement("-- name: GroupByNameForShare :one"))
		if _, err := s.PersonalGroup(context.Background(), "alice", "boss"); err == nil {
			t.Fatal("read failure became a new group")
		}
		if groupRecorded(t, s.db, "alice") {
			t.Fatal("read failure recorded a group")
		}
	})
}
