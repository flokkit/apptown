package conversation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/ids"
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
	if total, err := ledger.ProjectsTotal(t.Context(), db, idstest.N(1)); err != nil || total.Total != 383 || total.CostMicros != 406 {
		t.Fatalf("fixture spend: %+v %v", total, err)
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
	// sql-guarantee: One stored row shows a second harvest of this single-model record wrote nothing.
	_ = db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM token_usage WHERE session_id='t'`).Scan(&n)
	if n != 1 {
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
	if total, err := ledger.ProjectsTotal(t.Context(), db, idstest.N(1)); err != nil || total.Total != 383 || total.CostMicros != 406 {
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
	rows, err := db.SQL().QueryContext(context.Background(), `SELECT DISTINCT actor_principal::text, actor_via FROM token_usage WHERE session_id='t'`)
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
	restore := dbtest.NextStatementFailure(db, "INSERT INTO token_usage")
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
	if total, err := ledger.ProjectsTotal(t.Context(), db, idstest.N(1)); err != nil || total.Total != 383 {
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

// A cache refresh is a top-level usage entry, with its own model. Cursor
// advancement must make an unchanged record cost the same on a second read.
func TestHarvestIncludesCacheRefreshOnce(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	ledger := usage.New(db)
	s, _ := recordSession(t, []string{`{"type":"usage","id":"warm","timestamp":"2026-11-04T00:00:00.000Z","kind":"cache_warm","provider":"p","model":"warm","usage":{"input":2,"cacheRead":100,"totalTokens":102,"cost":{"total":0.001,"totalFromProvider":true}}}` + "\n",
		`{"type":"message","id":"answer","message":{"role":"assistant","provider":"q","model":"answer","usage":{"input":10,"output":5,"totalTokens":15,"cost":{"total":0.002}}}}` + "\n"})
	s.Labels = map[string]string{ProjectLabel: idstest.N(1).String()}
	for range 2 {
		if err := HarvestUsage(t.Context(), ledger, s); err != nil {
			t.Fatal(err)
		}
	}
	if total, err := ledger.ProjectsTotal(t.Context(), db, idstest.N(1)); err != nil || total.Total != 117 || total.CostMicros != 3000 {
		t.Fatalf("cache refresh spend: %+v %v", total, err)
	}
	// sql-guarantee: rollups cannot prove each provider/model kept its own row.
	rows, err := db.SQL().QueryContext(t.Context(), `SELECT provider, model, total, cost_micros FROM token_usage WHERE session_id='t' ORDER BY provider`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for _, want := range []struct {
		provider, model string
		total, cost     int64
	}{{"p", "warm", 102, 1000}, {"q", "answer", 15, 2000}} {
		var provider, model string
		var total, cost int64
		if !rows.Next() {
			t.Fatalf("missing spend row: %v", rows.Err())
		}
		if err := rows.Scan(&provider, &model, &total, &cost); err != nil {
			t.Fatal(err)
		}
		if provider != want.provider || model != want.model || total != want.total || cost != want.cost {
			t.Fatalf("spend row: %s/%s %d %d; want %+v", provider, model, total, cost, want)
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatalf("unexpected spend rows: %v", rows.Err())
	}
}

func TestHarvestRetainsMixedPriceSourcesExactlyOnce(t *testing.T) {
	db := databaseSuite.New(t)
	ledger := usage.New(db)
	lines := []string{"{\"type\":\"session\",\"version\":3,\"id\":\"test\"}\n"}
	for _, cost := range []string{`{"total":0.002,"totalFromProvider":true}`, `{"total":0.001}`, `{"total":0}`, `{"total":0,"totalFromProvider":true}`} {
		lines = append(lines, `{"type":"message","message":{"role":"assistant","provider":"custom","model":"m","usage":{"input":10,"output":1,"totalTokens":11,"cost":`+cost+`}}}`+"\n")
	}
	s, _ := recordSession(t, lines)
	for range 2 {
		if err := HarvestUsage(t.Context(), ledger, s); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := ledger.Summary(t.Context(), 1, ids.Nil, "")
	if err != nil || rep.Totals.ProviderReported != 2 || rep.Totals.Catalog != 1 || rep.Totals.Unpriced != 1 || rep.Totals.CostMicros != 3000 {
		t.Fatalf("harvest: %+v %v", rep, err)
	}
}
