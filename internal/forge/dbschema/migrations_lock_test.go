package dbschema

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

//go:embed migrations.lock
var migrationsLock string

// migrationDigest is what a migration says: its number and statements.
func migrationDigest(m migration) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00", m.n)
	for _, s := range m.stmts {
		fmt.Fprintf(h, "%d:%s\x00", len(s), s)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// A migration, once written, keeps its number and its statements: the
// lock records each one, the chain matches it, and the lock only grows
// (scripts/migrations-append-only.sh holds that against origin/main). A
// database upgraded by a migration is never upgraded by it again, so a
// renumbered or rewritten one would leave forges that ran the old one
// different from forges that run the new one.
//
// Kills: renumbering a migration, editing one already written, or adding
// one without recording it.
func TestMigrationsMatchTheirLock(t *testing.T) {
	t.Parallel()
	lines := strings.Split(strings.TrimSpace(migrationsLock), "\n")
	if want := fmt.Sprintf("baseline %d", schemaBaseline); lines[0] != want {
		t.Fatalf("migrations.lock begins %q; the baseline is %q", lines[0], want)
	}
	// One line per migration, in the chain's order: a second line for a
	// number, a missing one or one out of place is refused.
	locked := lines[1:]
	for i, m := range migrations {
		want := fmt.Sprintf("%d %s", m.n, migrationDigest(m))
		switch {
		case i >= len(locked):
			t.Errorf("migration %d is not in migrations.lock: append the line %q", m.n, want)
		case locked[i] != want:
			t.Errorf("migrations.lock line %d is %q, not migration %d's %q: a written migration is never changed, renumbered or recorded twice; add a new one", i+2, locked[i], m.n, want)
		}
	}
	for _, l := range locked[min(len(migrations), len(locked)):] {
		t.Errorf("migrations.lock records %q, which the chain does not have: a migration is never removed or renumbered", l)
	}
}
