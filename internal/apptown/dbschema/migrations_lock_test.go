package dbschema

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

//go:embed migrations.lock
var migrationsLock string

//go:embed schema.go
var migrationsGo string

//go:embed retired_workflows215.go
var retirement215Go string

// migrationDigest is what a migration says: its number and statements.
func migrationDigest(m migration) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00", m.n)
	for _, s := range m.stmts {
		fmt.Fprintf(h, "%d:%s\x00", len(s), s)
	}
	if m.data != nil {
		// Data migrations are locked as code as well as SQL. Keep the frozen
		// helper with its migration rather than depending on current readers.
		file, err := parser.ParseFile(token.NewFileSet(), "schema.go", migrationsGo, 0)
		if err != nil {
			panic(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			var number string
			var body ast.Expr
			for _, field := range lit.Elts {
				kv, ok := field.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				if key.Name == "n" {
					if v, ok := kv.Value.(*ast.BasicLit); ok {
						number = v.Value
					}
				}
				if key.Name == "data" {
					body = kv.Value
				}
			}
			if number == fmt.Sprint(m.n) && body != nil {
				var source bytes.Buffer
				if err := format.Node(&source, token.NewFileSet(), body); err != nil {
					panic(err)
				}
				fmt.Fprintf(h, "data:%s\x00", source.String())
				return false
			}
			return true
		})
		if m.n == 215 {
			fmt.Fprintf(h, "helpers:%s\x00", retirement215Go)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// A migration, once written, keeps its number and its statements: the
// lock records each one, the chain matches it, and the lock only grows
// (scripts/migrations-append-only.sh holds that against origin/main). A
// database upgraded by a migration is never upgraded by it again, so a
// renumbered or rewritten one would leave AppTown installations that ran the old one
// different from AppTown installations that run the new one.
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
