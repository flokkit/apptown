package identity

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/database"
)

func vetoGroupWrite(t *testing.T, db *database.DB, table, operation string) func() {
	t.Helper()
	if _, err := db.SQL().ExecContext(context.Background(), `CREATE FUNCTION veto_group_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(context.Background(), `CREATE TRIGGER veto_group BEFORE `+operation+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION veto_group_write()`); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if _, err := db.SQL().ExecContext(context.Background(), `DROP TRIGGER veto_group ON `+table); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGroupSaveRollsBackEveryRequiredWrite(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, marker, table, operation string }{
		{name: "name observation fails", marker: "-- name: TouchUser"},
		{name: "receipt fails", marker: "INSERT INTO audit_log"},
		{name: "name skipped", table: "users", operation: "INSERT"},
		{name: "group update skipped", table: "groups", operation: "UPDATE"},
		{name: "member insertion skipped", table: "group_members", operation: "INSERT"},
		{name: "member removal skipped", table: "group_members", operation: "DELETE"},
		{name: "receipt skipped", table: "audit_log", operation: "INSERT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s := New(testDB(t), Config{OpenInstance: true})
			testGrantRoot(t, s, "ops")
			if err := s.SaveGroup(ctx, GroupChange{Name: "kids", Description: "before", Members: []string{"old-member"}}, admittedOpenPrincipal(t, s, "ops")); err != nil {
				t.Fatal(err)
			}
			beforeGroups, err := s.Groups(ctx)
			if err != nil {
				t.Fatal(err)
			}
			beforeUsers, err := s.Users(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var restore func()
			if tc.marker != "" {
				restore = dbtest.NextStatementFailure(s.db, tc.marker)
			} else {
				restore = vetoGroupWrite(t, s.db, tc.table, tc.operation)
			}
			in := GroupChange{Name: "kids", Description: "after", Members: []string{"new-member"}}
			if err := s.SaveGroup(ctx, in, admittedOpenPrincipal(t, s, "ops")); err == nil {
				t.Fatal("accepted an incomplete group save")
			}
			groups, err := s.Groups(ctx)
			if err != nil {
				t.Fatal(err)
			}
			users, err := s.Users(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeGroups, groups) || !reflect.DeepEqual(beforeUsers, users) {
				t.Fatal("failed save changed the group or account records")
			}
			if receipts := ledger(t, s.db, "group.saved"); len(receipts) != 1 {
				t.Fatalf("failed save left a receipt: %+v", receipts)
			}
			restore()
			if err := s.SaveGroup(ctx, in, admittedOpenPrincipal(t, s, "ops")); err != nil {
				t.Fatalf("retry failed: %v", err)
			}
			groups, err = s.Groups(ctx)
			if err != nil || len(groups) != 1 || groups[0].Description != "after" || !reflect.DeepEqual(groups[0].Members, []string{"new-member"}) {
				t.Fatalf("retry did not replace the group: %+v %v", groups, err)
			}
		})
	}
}

func TestGroupDeleteRequiresCompleteRemoval(t *testing.T) {
	t.Parallel()
	for _, table := range []string{"group_members", "groups", "audit_log"} {
		t.Run(table, func(t *testing.T) {
			t.Parallel()
			s := New(testDB(t), Config{OpenInstance: true})
			testGrantRoot(t, s, "ops")
			if err := s.SaveGroup(context.Background(), GroupChange{Name: "kids", Members: []string{"amy"}}, admittedOpenPrincipal(t, s, "ops")); err != nil {
				t.Fatal(err)
			}
			op := "DELETE"
			if table == "audit_log" {
				op = "INSERT"
			}
			restore := vetoGroupWrite(t, s.db, table, op)
			if err := s.DeleteGroup(context.Background(), "kids", admittedOpenPrincipal(t, s, "ops")); err == nil {
				t.Fatal("accepted incomplete group deletion")
			}
			groups, err := s.Groups(context.Background())
			if err != nil || len(groups) != 1 || !reflect.DeepEqual(groups[0].Members, []string{"amy"}) {
				t.Fatalf("failed deletion changed the group: %+v %v", groups, err)
			}
			if receipts := ledger(t, s.db, "group.deleted"); len(receipts) != 0 {
				t.Fatalf("failed deletion left a receipt: %+v", receipts)
			}
			restore()
			if err := s.DeleteGroup(context.Background(), "kids", admittedOpenPrincipal(t, s, "ops")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConcurrentGroupSavesKeepOneWholeMembership(t *testing.T) {
	t.Parallel()
	s := New(testDB(t), Config{OpenInstance: true})
	testGrantRoot(t, s, "ops")
	inputs := []GroupChange{
		{Name: "kids", Description: "first", Members: []string{"zoe", "amy", "first-only", "zoe"}},
		{Name: "kids", Description: "second", Members: []string{"amy", "zoe", "second-only"}},
	}
	start := make(chan struct{})
	errs := make(chan error, len(inputs))
	var wg sync.WaitGroup
	for _, in := range inputs {
		wg.Add(1)
		go func(in GroupChange) {
			defer wg.Done()
			<-start
			errs <- s.SaveGroup(context.Background(), in, admittedOpenPrincipal(t, s, "ops"))
		}(in)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	groups, err := s.Groups(context.Background())
	if err != nil || len(groups) != 1 {
		t.Fatalf("saved group: %+v %v", groups, err)
	}
	want := []string{"amy", "first-only", "zoe"}
	if groups[0].Description == "second" {
		want = []string{"amy", "second-only", "zoe"}
	} else if groups[0].Description != "first" {
		t.Fatal("unknown saved description")
	}
	if !reflect.DeepEqual(groups[0].Members, want) {
		t.Fatalf("mixed concurrent member lists: %v", groups[0].Members)
	}
	if receipts := ledger(t, s.db, "group.saved"); len(receipts) != 2 {
		t.Fatalf("concurrent receipts: %+v", receipts)
	}
}
