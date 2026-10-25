package conversation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/console"
)

// The fixture is a real pi session shape (version 3): a session header, a
// model_change and thinking_level_change mark, then user / assistant /
// toolResult messages — one assistant turn that only thinks, one that
// calls a tool, one that answers with text and paid usage.
const piFixture = "../../console/testdata/pisession/console.jsonl"

func fixtureLines(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(piFixture)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.SplitAfter(string(raw), "\n") {
		if l != "" {
			lines = append(lines, l) // each line keeps its newline: the reader treats a bare tail as partial
		}
	}
	return lines
}

// recordSession stages the fixture as a console's session record and
// returns the session handle and the record's path.
func recordSession(t *testing.T, lines []string) (*console.Session, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sessions", "2026-05-15T09-57-43-861Z_console.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o644); err != nil {
		t.Fatal(err)
	}
	return &console.Session{Meta: console.Meta{ID: "t", AgentSession: console.AgentSession}, Dir: dir}, path
}

// Accounting reads the same record through the same reader, resuming from
// the cursor it stored: two harvests of an unchanged record write once.
func TestHarvestConsoleUsageFromRecord(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	ledger := usage.New(db)
	s, path := recordSession(t, fixtureLines(t))
	s.Labels = map[string]string{ProjectLabel: idstest.N(1).String()}
	if err := HarvestUsage(context.Background(), ledger, s); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: One stored row per paid provider for this session is the guarantee; the usage rollups sum rows and cannot show how many were written.
	rows, err := db.Query(`SELECT provider, model, total, cost_micros FROM token_usage WHERE session_id='t' ORDER BY provider`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct {
		provider, model string
		total, cost     int64
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.provider, &r.model, &r.total, &r.cost); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 2 || got[0].provider != "ds4" || got[0].total != 1833+1912 ||
		got[1].provider != "openrouter" || got[1].total != 2660 || got[1].cost != 1200 {
		t.Fatalf("ledger rows: %+v", got)
	}
	off, err := ledger.Cursor(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if off != fi.Size() {
		t.Errorf("cursor must sit at EOF: %d vs %d", off, fi.Size())
	}
	if err := HarvestUsage(context.Background(), ledger, s); err != nil {
		t.Fatal(err)
	}
	var n int
	// sql-guarantee: As above: the row count, not a rollup, shows a second harvest wrote nothing.
	_ = db.QueryRow(`SELECT COUNT(*) FROM token_usage WHERE session_id='t'`).Scan(&n)
	if n != 2 {
		t.Errorf("a second harvest of an unchanged record must add nothing, got %d rows", n)
	}
}

// A chat's last harvest runs as the chat ends or its project is cleaned up,
// and the record is removed after it: a harvest whose caller stopped still
// records the spend and the cursor before the source is gone.
//
// Kills: the cursor read on the stopped caller, skipping the record.
func TestTheLastHarvestRecordsSpendThoughTheCallerStopped(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	ledger := usage.New(db)
	s, path := recordSession(t, fixtureLines(t))
	s.Labels = map[string]string{ProjectLabel: idstest.N(1).String()}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stopped, cancel := context.WithCancel(t.Context())
	cancel()
	if err := HarvestUsage(stopped, ledger, s); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(s.Dir); err != nil { // the chat ends; its record goes
		t.Fatal(err)
	}
	if total, err := ledger.ProjectsTotal(t.Context(), db, idstest.N(1)); err != nil || total.Total != 1833+1912+2660 || total.CostMicros != 1200 {
		t.Fatalf("spend after a stopped last harvest: %+v %v", total, err)
	}
	if off, err := ledger.Cursor(t.Context(), path); err != nil || off != fi.Size() {
		t.Fatalf("cursor after a stopped last harvest: %d %v, want %d", off, err, fi.Size())
	}
}

// A chat's spend is its opener's, as the chat recorded them, and the chat
// is how they spent it: harvested with no caller after alice's name is
// removed and given to someone else, it still names her.
func TestHarvestedChatSpendIsTheOpeners(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	ctx := context.Background()
	people := identity.New(db, identity.Config{})
	for _, name := range []string{"ops", "alice"} {
		if _, err := people.EnsureUser(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := people.PutGrant(ctx, identity.Spec{SubjectType: "user", Subject: "ops", Role: access.RoleInstanceAdmin, Scope: access.ScopeInstance, By: "fixture"}); err != nil {
		t.Fatal(err)
	}
	alice, err := identity.ActorIn(ctx, db, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := people.RemoveName(ctx, "alice", testAdmittedPrincipal(t, people, "ops")); err != nil {
		t.Fatal(err)
	}
	if _, err := people.EnsureUser(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	s, _ := recordSession(t, fixtureLines(t))
	s.Actor = "alice"
	s.Labels = map[string]string{ProjectLabel: idstest.N(1).String(), OpenerLabel: alice.Principal.String()}
	if err := HarvestUsage(ctx, usage.New(db), s); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: the stored spend line's actor is the guarantee; the usage rollups do not return who spent.
	rows, err := db.Query(`SELECT DISTINCT actor_principal::text, actor_via FROM token_usage WHERE session_id='t'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var principal, via string
		if err := rows.Scan(&principal, &via); err != nil {
			t.Fatal(err)
		}
		if principal != alice.Principal.String() || via != access.ViaChat+":t" {
			t.Fatalf("the chat's spend names %s via %q", principal, via)
		}
		n++
	}
	if n != 1 {
		t.Fatalf("spend lines by %d actors", n)
	}
}

// Spend is never skipped: a harvest whose write fails returns the failure and
// leaves its cursor, so the next harvest records that spend once; a record
// folder that cannot be read is a failure, never an empty harvest.
//
// Kills: logging the failed write and advancing the cursor past it; reading
// an unreadable folder as no records.
func TestAFailedHarvestKeepsItsSpendForTheNext(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	ledger := usage.New(db)
	s, path := recordSession(t, fixtureLines(t))
	s.Labels = map[string]string{ProjectLabel: idstest.N(1).String()}
	restore := db.FailNextStatement("INSERT INTO token_usage")
	if err := HarvestUsage(t.Context(), ledger, s); err == nil {
		t.Fatal("a failed spend write harvested")
	}
	restore()
	if off, err := ledger.Cursor(t.Context(), path); err != nil || off != 0 {
		t.Fatalf("the cursor passed unrecorded spend: %d %v", off, err)
	}
	if err := HarvestUsage(t.Context(), ledger, s); err != nil {
		t.Fatal(err)
	}
	if total, err := ledger.ProjectsTotal(t.Context(), db, idstest.N(1)); err != nil || total.Total != 1833+1912+2660 {
		t.Fatalf("spend after the retry: %+v %v", total, err)
	}
	unreadable := &console.Session{Meta: console.Meta{ID: "u", AgentSession: console.AgentSession}, Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(unreadable.Dir, "sessions"), []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := HarvestUsage(t.Context(), ledger, unreadable); err == nil {
		t.Fatal("an unreadable record folder harvested nothing, as if it held no records")
	}
}
