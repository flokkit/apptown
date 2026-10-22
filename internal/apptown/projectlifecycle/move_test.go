package projectlifecycle

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// Every project-scoped table, one with a project_id (or an eval's
// source_project_id) or one reached by a foreign key from such a table, is
// classified for a move, and nothing is classified that is not one: a new
// project table fails here until someone decides whether it moves.
func TestEveryProjectTableIsClassifiedForAMove(t *testing.T) {
	fragments, err := filepath.Glob(filepath.Join("..", "*", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	tableRe := regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS (\w+) \((.*?)\n\);`)
	scopeRe := regexp.MustCompile(`(?m)^\s*(project_id|source_project_id)\b`)
	refRe := regexp.MustCompile(`REFERENCES (\w+)`)
	bodies := map[string]string{}
	for _, f := range fragments {
		if filepath.Base(filepath.Dir(f)) == "dbschema" {
			continue // the assembled schema repeats the owners' fragments
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range tableRe.FindAllStringSubmatch(string(raw), -1) {
			bodies[m[1]] = m[2]
		}
	}
	if len(bodies) < 50 {
		t.Fatalf("read only %d tables from %d fragments; the schema moved", len(bodies), len(fragments))
	}
	scoped := map[string]bool{"projects": true}
	for table, body := range bodies {
		if scopeRe.MatchString(body) {
			scoped[table] = true
		}
	}
	for grew := true; grew; {
		grew = false
		for table, body := range bodies {
			if scoped[table] {
				continue
			}
			for _, ref := range refRe.FindAllStringSubmatch(body, -1) {
				if scoped[ref[1]] {
					scoped[table], grew = true, true
					break
				}
			}
		}
	}
	var unclassified, stale []string
	for table := range scoped {
		if c, ok := moveRegistry[table]; !ok || c.Reason == "" {
			unclassified = append(unclassified, table)
		}
	}
	for table, c := range moveRegistry {
		if !scoped[table] {
			stale = append(stale, table)
		}
		if c.Class != MoveExported && c.Class != MoveListed && c.Class != MoveExcluded {
			t.Errorf("%s has no known class: %q", table, c.Class)
		}
	}
	sort.Strings(unclassified)
	sort.Strings(stale)
	if len(unclassified) > 0 || len(stale) > 0 {
		t.Fatalf("classify for a move (move.go): %v; no longer project tables: %v", unclassified, stale)
	}
}
