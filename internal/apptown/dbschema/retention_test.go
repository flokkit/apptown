package dbschema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This checks reference coverage, not the semantics of retention predicates.
func TestEveryWorkReferenceHasAnOwnerRetentionDecision(t *testing.T) {
	raw, err := os.ReadFile("fragments.json")
	if err != nil {
		t.Fatal(err)
	}
	var fragments []string
	if err := json.Unmarshal(raw, &fragments); err != nil {
		t.Fatal(err)
	}
	tables := regexp.MustCompile(`(?is)CREATE TABLE (?:IF NOT EXISTS )?(\w+)\s*\((.*?)\n\);`)
	columns := regexp.MustCompile(`(?m)^\s*(\w+)\s+\w+`)
	references := regexp.MustCompile(`^(change_id|.*_change_id|run_id|.*_run_id|source_run|change_ids_json)$`)
	from := regexp.MustCompile(`(?i)\bFROM\s+(\w+)(?:\s+(\w+))?\s+WHERE\s+`)
	args := regexp.MustCompile(`sqlc\.arg\(\w+\)`)
	exceptions := map[string]string{
		"conformance.scan_observations.change_id": "Scan observations preserve independent history after work disposal.",
		"conformance.scan_observations.run_id":    "Scan observations preserve independent history after work disposal.",
	}
	for _, fragment := range fragments {
		owner := filepath.Base(filepath.Dir(fragment))
		if owner == "workflow" {
			continue
		}
		ddl, err := os.ReadFile(filepath.Join("../../..", fragment))
		if err != nil {
			t.Fatal(err)
		}
		queries, err := filepath.Glob(filepath.Join("..", owner, "internal", "*", "*.sql"))
		if err != nil {
			t.Fatal(err)
		}
		var retention string
		for _, path := range queries {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			_, body, ok := strings.Cut(string(data), "-- name: RetainsAutomatedWork :one")
			if ok {
				body, _, _ = strings.Cut(body, ";")
				retention += body
			}
		}
		blocks := tables.FindAllStringSubmatch(string(ddl), -1)
		if len(blocks) != len(regexp.MustCompile(`(?im)^\s*CREATE\s+TABLE\b`).FindAll(ddl, -1)) {
			t.Fatalf("%s: unsupported table declaration", fragment)
		}
		for _, table := range blocks {
			for _, column := range columns.FindAllStringSubmatch(table[2], -1) {
				col := column[1]
				if !references.MatchString(col) {
					continue
				}
				key := owner + "." + table[1] + "." + col
				if _, ok := exceptions[key]; ok {
					delete(exceptions, key)
					continue
				}
				covered := false
				for _, loc := range from.FindAllStringSubmatchIndex(retention, -1) {
					if retention[loc[2]:loc[3]] != table[1] {
						continue
					}
					alias := ""
					if loc[4] >= 0 {
						alias = retention[loc[4]:loc[5]]
					}
					clause := retention[loc[1]:]
					depth := 0
					for i, c := range clause {
						if c == '(' {
							depth++
						}
						if c == ')' {
							depth--
							if depth < 0 {
								clause = clause[:i]
								break
							}
						}
					}
					clause = args.ReplaceAllString(clause, "")
					pattern := `(?:^|[^.\w])` + col + `\b`
					if alias != "" {
						pattern = `\b` + alias + `\.` + col + `\b`
					}
					covered = covered || regexp.MustCompile(pattern).MatchString(clause)
				}
				if !covered {
					t.Errorf("%s has no retention predicate for its table", key)
				}
			}
		}
	}
	for key := range exceptions {
		t.Errorf("stale retention exception: %s", key)
	}
}
