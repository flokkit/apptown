package dbschema

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database"
)

// Migration 163 rewrites what the removed readers tolerated into the shape
// the code reads now, and removes what only removed features kept:
//   - a copy of the app's volume that named no volume names it as the item,
//     and the set's other items, their order and their other fields stay;
//   - a need instance still marked pending is creating; one in another state
//     is untouched;
//   - a deployment whose readiness was needs-reconcile is uncertain, so a
//     person checks it, never failed or reconciled;
//   - a live grant of a removed capability is revoked on the day it went and
//     kept; a revoked one keeps its time; another capability's is untouched;
//   - the local-Docker start record and the draft model slot's rows go.
//
// Kills: a rewrite that fills a volume an item already names, reorders or
// drops items, maps needs-reconcile to failed, deletes grants instead of
// revoking them, or touches rows it should not.
func TestMigration163RewritesOldRecordsAndRemovesWhatOnlyRemovedFeaturesKept(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	var project, deployment string
	if err := db.QueryRow(`SELECT id::text, project_id::text FROM deployments ORDER BY id LIMIT 1`).Scan(&deployment, &project); err != nil {
		t.Fatalf("the release's records hold a deployment: %v", err)
	}
	const (
		oldSet   = "01a16000-0000-7000-8000-000000000001"
		newSet   = "01a16000-0000-7000-8000-000000000002"
		pending  = "01a16000-0000-7000-8000-000000000003"
		ready    = "01a16000-0000-7000-8000-000000000004"
		liveCap  = "01a16000-0000-7000-8000-000000000005"
		oldCap   = "01a16000-0000-7000-8000-000000000006"
		otherCap = "01a16000-0000-7000-8000-000000000007"
	)
	oldItems := `[{"kind":"volume","name":"forge-data-old","file":"data.tar.gz"},{"kind":"need","name":"postgres","file":"postgres.dump"},{"kind":"volume","name":"second","volume":null}]`
	newItems := `[{"kind":"volume","name":"apptown-data","volume":"apptown-data-x","file":"data.tar.gz"}]`
	// sql-guarantee: the release's records before the migration; the build
	// that wrote them is not this one, so no owner operation can.
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO backups (id, project_id, deployment, taken_at, items_json) VALUES (?, ?, 'main', '2026-09-01T00:00:00Z', ?::jsonb)`, []any{oldSet, project, oldItems}},
		{`INSERT INTO backups (id, project_id, deployment, taken_at, items_json) VALUES (?, ?, 'main', '2026-09-02T00:00:00Z', ?::jsonb)`, []any{newSet, project, newItems}},
		{`INSERT INTO need_instances (id, deployment_id, need, updated_at) VALUES (?, ?, 'old-need', '2026-09-01T00:00:00Z')`, []any{pending, deployment}},
		{`INSERT INTO need_instances (id, deployment_id, need, status, updated_at) VALUES (?, ?, 'ready-need', 'ready', '2026-09-01T00:00:00Z')`, []any{ready, deployment}},
		{`UPDATE deployments SET readiness = 'needs-reconcile' WHERE id = ?`, []any{deployment}},
		{`INSERT INTO grants (id, subject_type, subject, capability, scope_type, created_at) VALUES (?, 'user', 'alice', 'deployment:open-restored', 'instance', '2026-09-01T00:00:00Z')`, []any{liveCap}},
		{`INSERT INTO grants (id, subject_type, subject, capability, scope_type, created_at, revoked_at) VALUES (?, 'user', 'bob', 'standards:manage-catalog', 'instance', '2026-09-01T00:00:00Z', '2026-09-10T00:00:00Z')`, []any{oldCap}},
		{`INSERT INTO grants (id, subject_type, subject, capability, scope_type, created_at) VALUES (?, 'user', 'carol', 'deployment:restore', 'instance', '2026-09-01T00:00:00Z')`, []any{otherCap}},
		{`INSERT INTO instance_settings (key, value) VALUES ('local_docker', 'drained') ON CONFLICT (key) DO UPDATE SET value = excluded.value`, nil},
		{`INSERT INTO slot_fallbacks (slot, fallback) VALUES ('draft', 'fast') ON CONFLICT (slot) DO NOTHING`, nil},
		{`INSERT INTO model_slots (slot, profile, pattern, updated_at) VALUES ('draft', 'standard', 'x/y', '2026-09-01T00:00:00Z')`, nil},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	items := func(id string) []map[string]any {
		var raw []byte
		if err := db.QueryRow(`SELECT items_json FROM backups WHERE id = ?`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var out []map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	old := items(oldSet)
	if len(old) != 3 || old[0]["volume"] != "forge-data-old" || old[0]["file"] != "data.tar.gz" ||
		old[1]["kind"] != "need" || old[1]["volume"] != nil || old[2]["volume"] != "second" {
		t.Fatalf("the old set's items after 163: %v", old)
	}
	if got := items(newSet); len(got) != 1 || got[0]["volume"] != "apptown-data-x" {
		t.Fatalf("a set that names its volume after 163: %v", got)
	}

	status := func(id string) string {
		var s string
		if err := db.QueryRow(`SELECT status FROM need_instances WHERE id = ?`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if got := status(pending); got != "creating" {
		t.Fatalf("a pending need after 163: %q", got)
	}
	if got := status(ready); got != "ready" {
		t.Fatalf("a ready need after 163: %q", got)
	}
	var readiness string
	if err := db.QueryRow(`SELECT readiness FROM deployments WHERE id = ?`, deployment).Scan(&readiness); err != nil || readiness != "uncertain" {
		t.Fatalf("a needs-reconcile deployment after 163: %q %v", readiness, err)
	}

	revoked := func(id string) *time.Time {
		var at *time.Time
		if err := db.QueryRow(`SELECT revoked_at FROM grants WHERE id = ?`, id).Scan(&at); err != nil {
			t.Fatalf("grant %s is kept: %v", id, err)
		}
		return at
	}
	day := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	if at := revoked(liveCap); at == nil || !at.Equal(day("2026-10-23T00:00:00Z")) {
		t.Fatalf("a live grant of a removed capability after 163: %v", at)
	}
	if at := revoked(oldCap); at == nil || !at.Equal(day("2026-09-10T00:00:00Z")) {
		t.Fatalf("a revoked grant of a removed capability after 163: %v", at)
	}
	if at := revoked(otherCap); at != nil {
		t.Fatalf("a grant of a kept capability after 163: %v", at)
	}

	for what, q := range map[string]string{
		"the local-Docker start record": `SELECT count(*) FROM instance_settings WHERE key = 'local_docker'`,
		"the draft slot's fallback":     `SELECT count(*) FROM slot_fallbacks WHERE slot = 'draft'`,
		"the draft slot's models":       `SELECT count(*) FROM model_slots WHERE slot = 'draft'`,
	} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s after 163: %d %v", what, n, err)
		}
	}
	wantNeedStatusRequired(t, db, deployment)
}

// wantNeedStatusRequired: a need instance must say its state; the column
// has no default to fall back on.
func wantNeedStatusRequired(t *testing.T, db *database.DB, deployment string) {
	t.Helper()
	// sql-guarantee: the storage rule itself.
	if _, err := db.Exec(`INSERT INTO need_instances (id, deployment_id, need, updated_at) VALUES ('01a16000-0000-7000-8000-0000000000ff', ?, 'unsaid', '2026-10-23T00:00:00Z')`, deployment); err == nil {
		t.Error("a need instance naming no state was stored")
	}
}
