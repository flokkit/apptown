package dbschema

import (
	"context"
	"fmt"
	"slices"
	"testing"
)

// Migration 187 deletes grants of the removed roles and at the removed
// scopes, which confer nothing any more, and leaves every other grant.
//
// Kills: the DELETE removed, a removed role or scope left out of it, or a
// kept grant caught by it.
func TestMigration187DeletesGrantsThatConferNothing(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	before := slices.IndexFunc(migrations, func(m migration) bool { return m.n == 187 })
	if _, err := applyMigrations(t.Context(), db, migrations[:before]); err != nil {
		t.Fatal(err)
	}
	at := "'2026-10-01T00:00:00Z'"
	id := func(n int) string { return fmt.Sprintf("'01a1323b-3213-7000-8853-c2d607476%03d'", n) }
	type row struct{ role, capability, scope, scopeID string }
	gone := []row{
		{"integration-administrator", "", "instance", ""}, {"group-viewer", "", "group", "kids"}, {"group-project-creator", "", "group", "kids"},
		{"group-maintainer", "", "group", "kids"}, {"group-auditor", "", "group", "kids"},
		{"", "change:read", "change", "c"}, {"", "run:read", "run", "r"}, {"", "conformance:read", "dimension", "security"},
	}
	kept := []row{
		{"release-manager", "", "release-target", "*"}, {"auditor", "", "instance", ""}, {"group-owner", "", "group", "kids"},
		{"project-viewer", "", "project", "p"}, {"", "release:promote", "release-target", "*"},
	}
	// sql-guarantee: grants of removed roles and scopes that this build
	// refuses to record; only an earlier release could have written them.
	for i, g := range append(append([]row{}, gone...), kept...) {
		if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO grants (id, subject_type, subject, role_name, capability, scope_type, scope_id, created_at) VALUES (`+
			id(i)+`, 'user', 'alice', '`+g.role+`', '`+g.capability+`', '`+g.scope+`', '`+g.scopeID+`', `+at+`)`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:before+1]); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM grants WHERE subject = 'alice'`).Scan(&left); err != nil || left != len(kept) {
		t.Fatalf("%d grants left, want the %d kept: %v", left, len(kept), err)
	}
}
