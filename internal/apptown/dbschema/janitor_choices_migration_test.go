package dbschema

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
)

func TestMigration242ConvertsSavedApprovalsAndKeepsHistory(t *testing.T) {
	for _, saved := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "saved approvals"}[saved], func(t *testing.T) {
			db := releaseDB(t)
			if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 242)]); err != nil {
				t.Fatal(err)
			}
			at := time.Now().UTC().Truncate(time.Second)
			principal := ids.New(at)
			var oldIDs []ids.ID
			if saved {
				for _, packs := range []bool{false, true} {
					id := ids.New(at)
					oldIDs = append(oldIDs, id)
					raw, err := json.Marshal(map[string]any{"ruleId": id, "revision": 1, "enabled": true, "issuer": map[string]any{"name": "Sam", "principal": principal, "via": "session"}, "changedAt": at, "question": map[string]any{"definition": map[string]any{"scope": "instance", "categories": map[string]any{"dependencies": "aggressive", "securityFixes": !packs, "waysOfWorking": packs}}}})
					if err != nil {
						t.Fatal(err)
					}
					// Migration storage guarantee: only the old schema can produce these records.
					if _, err = db.SQL().ExecContext(t.Context(), `INSERT INTO standing_rules(id,current_revision) VALUES($1,1)`, id); err != nil {
						t.Fatal(err)
					}
					if _, err = db.SQL().ExecContext(t.Context(), `INSERT INTO standing_rule_revisions(rule_id,revision,enabled,record_json) VALUES($1,1,true,$2)`, id, string(raw)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := applyMigrations(t.Context(), db, migrations); err != nil {
				t.Fatal(err)
			}
			rows, err := db.SQL().QueryContext(t.Context(), `SELECT record_json FROM standing_rule_revisions ORDER BY rule_id,revision`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			count, enabled, historical := 0, 0, 0
			for rows.Next() {
				var raw []byte
				if err := rows.Scan(&raw); err != nil {
					t.Fatal(err)
				}
				count++
				var r workflow.StandingRevision
				if err := json.Unmarshal(raw, &r); err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				if len(fields) != 6 || fields["question"] != nil || r.Issuer.Principal != principal {
					t.Fatalf("record: %s", raw)
				}
				if r.Enabled {
					enabled++
					if r.Choices == (workflow.JanitorChoices{Dependencies: "aggressive", SecurityFixes: true, BaseImages: true, ToolchainPatches: true, Packs: true}) {
						continue
					}
				}
				for _, id := range oldIDs {
					if r.RuleID == id && r.Revision == 1 && r.At.Equal(at) {
						historical++
					}
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !saved && count != 0 || saved && (count != 5 || enabled != 3 || historical != 2) {
				t.Fatalf("records=%d enabled revisions=%d historical=%d", count, enabled, historical)
			}
			if saved {
				var raw []byte
				if err := db.SQL().QueryRowContext(t.Context(), `SELECT r.record_json FROM standing_rules h JOIN standing_rule_revisions r ON r.rule_id=h.id AND r.revision=h.current_revision WHERE r.enabled`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var r workflow.StandingRevision
				if err := json.Unmarshal(raw, &r); err != nil {
					t.Fatal(err)
				}
				if r.Revision != 1 || r.Issuer.Name != "Sam" || r.Issuer.Principal != principal || r.Choices != (workflow.JanitorChoices{Dependencies: "aggressive", SecurityFixes: true, BaseImages: true, ToolchainPatches: true, Packs: true}) {
					t.Fatalf("combined choices: %s", raw)
				}
			}
		})
	}
}
