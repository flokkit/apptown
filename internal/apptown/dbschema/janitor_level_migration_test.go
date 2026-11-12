package dbschema

import (
	"encoding/json"
	"github.com/flokkit/apptown/internal/ids"
	"testing"
	"time"
)

func TestMigration237PreservesProjectChoiceAttribution(t *testing.T) {
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 237)]); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	pid := ids.New(now)
	// Historical records cannot be produced by the current owner operation.
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO projects(id,slug,name,serving_label,created_at) VALUES($1,'levels','Levels','levels',$2)`, pid, now); err != nil {
		t.Fatal(err)
	}
	for _, off := range []bool{true, false} {
		id := ids.New(now)
		raw, _ := json.Marshal(map[string]any{"projectId": pid, "off": off, "person": map[string]string{"name": "sam"}, "at": "2026-11-12T00:00:00Z", "reason": "project choice"})
		// sql-guarantee: pre-migration boolean shape; the new operation has no off field.
		if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO standing_project_choices(id,project_id,off,record_json,at) VALUES($1,$2,$3,$4,$5)`, id, pid, off, string(raw), now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := applyMigrations(t.Context(), db, migrations); err != nil {
		t.Fatal(err)
	}
	rows, err := db.SQL().QueryContext(t.Context(), `SELECT level,record_json FROM standing_project_choices ORDER BY level`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var level string
		var raw []byte
		if err := rows.Scan(&level, &raw); err != nil {
			t.Fatal(err)
		}
		var record map[string]any
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		if level != "off" && level != "routine" || record["level"] != level || record["reason"] != "project choice" || record["at"] != "2026-11-12T00:00:00Z" || record["person"].(map[string]any)["name"] != "sam" {
			t.Fatalf("lost record: %s %s", level, raw)
		}
		if _, ok := record["off"]; ok {
			t.Fatal("boolean opt-out survived")
		}
		count++
	}
	if err := rows.Err(); err != nil || count != 2 {
		t.Fatalf("rows=%d %v", count, err)
	}
}
