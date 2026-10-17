package projectlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
)

func ownerVeto(t *testing.T, db *database.DB, table string) func() {
	t.Helper()
	if _, err := db.Exec(`CREATE FUNCTION veto_authorization_record() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER veto_authorization_record BEFORE INSERT ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION veto_authorization_record()`); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if _, err := db.Exec(`DROP TRIGGER veto_authorization_record ON ` + table); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectCreationRejectsRetiredPersonalGroup(t *testing.T) {
	t.Parallel()
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			f := newOwnerFixture(t, false)
			if fallback {
				if err := f.identity.SeedAdministrator(context.Background(), "boss"); err != nil {
					t.Fatal(err)
				}
				if err := f.identity.SaveGroup(context.Background(), identity.GroupChange{Name: "alice"}, access.Principal{Kind: "user", Name: "boss", Via: "session"}); err != nil {
					t.Fatal(err)
				}
			}
			name, err := f.identity.PersonalGroup(context.Background(), "alice", "boss")
			if err != nil {
				t.Fatal(err)
			}
			// Keep the recorded personal-group identity but retire it before
			// creation; deletion would erase the historical choice.
			// sql-guarantee: Keep the recorded personal-group identity but retire it before project creation; SaveGroup preserves retired_at and DeleteGroup would remove the historical choice.
			if _, err := f.db.Exec(`UPDATE groups SET retired_at=now() WHERE name=?`, name); err != nil {
				t.Fatal(err)
			}
			if err := f.identity.SeedAdministrator(context.Background(), "alice"); err != nil {
				t.Fatal(err)
			}
			if err := f.identity.EnsureDefaults(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			// The group is retired, not gone: its kind no longer reads as live,
			// and the recorded row (the historical ownership choice) remains.
			if kind, err := f.identity.GroupKind(context.Background(), name); err != nil || kind != "" {
				t.Fatalf("retired group still reads as live: %q %v", kind, err)
			}
			// sql-guarantee: identity reads hide retired groups and answer the same for a missing one; only the row shows retirement rather than deletion.
			ownerCount(t, f.db, 1, `SELECT COUNT(*) FROM groups WHERE name=? AND retired_at IS NOT NULL`, name)
			if _, err := f.lifecycle.Create(context.Background(), Draft{Slug: "retired-owner", Name: "P", Group: name, Actor: "alice"}); err == nil {
				t.Fatal("created a project in a retired group")
			}
		})
	}
}

func TestProjectCreationIncludesOwnershipAndReceipt(t *testing.T) {
	t.Parallel()
	for _, table := range []string{"projects", "project_adoptions", "grants", "audit_log"} {
		t.Run(table, func(t *testing.T) {
			f := newOwnerFixture(t, false)
			if err := f.identity.SeedAdministrator(context.Background(), "boss"); err != nil {
				t.Fatal(err)
			}
			drop := ownerVeto(t, f.db, table)
			if _, err := f.lifecycle.Create(context.Background(), Draft{Slug: "atomic", Name: "Atomic", Actor: "alice via console"}); err == nil {
				t.Fatal("accepted incomplete project creation")
			}
			if slugTaken(t, f.db, "atomic") {
				t.Fatal("rolled-back creation left its project")
			}
			// sql-guarantee: The rolled-back project has no id to ask the adoption owner about; only the table total proves no adoption row survived.
			ownerCount(t, f.db, 0, `SELECT COUNT(*) FROM project_adoptions`)
			grants, err := f.identity.AllGrants(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, g := range grants {
				if g.ScopeType == access.ScopeProject {
					t.Fatalf("rolled-back creation left a project grant: %+v", g)
				}
			}
			// sql-guarantee: identity reads list only unrevoked grants, so a rolled-back creation that left revoked grant history would look clean through them.
			ownerCount(t, f.db, 0, `SELECT COUNT(*) FROM grants WHERE scope_type='project'`)
			if created := lifecycleReceipts(t, f.db, "project.created"); len(created) != 0 {
				t.Fatalf("rolled-back creation left a receipt: %+v", created)
			}
			drop()
			// Through alice's chat: the chat acts for her principal, and the
			// project is hers.
			alice, err := f.identity.EnsurePerson(context.Background(), "alice")
			if err != nil {
				t.Fatal(err)
			}
			chat := access.WithCaller(context.Background(), access.Caller{Verified: true, Receipt: "alice via console",
				Principal: access.Principal{ID: alice.PrincipalID, Kind: "instrument", Name: "alice via console", Person: "alice", Instrument: "console", ConsoleID: "c"}})
			p, err := f.lifecycle.Create(chat, Draft{Slug: "atomic", Name: "Atomic", Actor: "alice via console"})
			if err != nil {
				t.Fatal(err)
			}
			stored, err := project.ByIDIn(context.Background(), f.db, p.ID)
			if err != nil || stored.OwnerGroup != "boss" || stored.DefaultApprovers != "role:project-approver,role:project-owner" {
				t.Fatalf("project ownership: %+v %v", stored, err)
			}
			atProject, err := f.identity.GrantsAt(context.Background(), access.ScopeProject, p.ID.String())
			if err != nil {
				t.Fatal(err)
			}
			owners := 0
			for _, g := range atProject {
				if g.RoleName == access.RoleProjectOwner && g.Subject == "alice" {
					owners++
				}
			}
			if owners != 1 {
				t.Fatalf("owner grants: %+v", atProject)
			}
			// sql-guarantee: identity reads list only unrevoked grants; creation must record exactly one owner grant row, not one live row beside others.
			ownerCount(t, f.db, 1, `SELECT COUNT(*) FROM grants_named WHERE scope_type='project' AND scope_id=? AND role_name=? AND subject='alice'`, p.ID.String(), access.RoleProjectOwner)
			created := lifecycleReceipts(t, f.db, "project.created")
			// A map, not a struct: encoding/json matches struct fields without
			// regard to case, and the receipt's key is exactly ownerGroup.
			var payload map[string]any
			if len(created) != 1 || created[0].Actor != "alice via console" || json.Unmarshal(created[0].Payload, &payload) != nil || payload["ownerGroup"] != "boss" {
				t.Fatalf("creation receipt: %+v", created)
			}
			if _, err := f.lifecycle.Create(context.Background(), Draft{Slug: "atomic", Name: "Duplicate", Actor: "alice"}); !errors.Is(err, project.ErrSlugTaken) {
				t.Fatalf("duplicate: %v", err)
			}
		})
	}
	t.Run("ProjectByID", func(t *testing.T) {
		f := newOwnerFixture(t, false)
		t.Cleanup(f.db.FailNextStatement("ProjectByID"))
		if _, err := f.lifecycle.Create(context.Background(), Draft{Slug: "unreadable", Name: "P", Actor: "alice"}); err == nil {
			t.Fatal("accepted failed required read")
		}
		if slugTaken(t, f.db, "unreadable") {
			t.Fatal("failed required read left its project")
		}
	})
}
