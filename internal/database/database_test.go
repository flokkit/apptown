package database_test

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/stamp"
)

var suite = dbtest.NewSuite(func(_ context.Context, url string) (*database.DB, error) { return database.Open(url) })

func testDB(t *testing.T) *database.DB { t.Helper(); return suite.New(t) }
func TestMain(m *testing.M)            { os.Exit(suite.Run(m)) }
func execTest(t *testing.T, db *database.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}
func countTest(t *testing.T, db *database.DB, want int, q string, args ...any) {
	t.Helper()
	var got int
	if err := db.QueryRow(q, args...).Scan(&got); err != nil || got != want {
		t.Fatalf("count: %d want %d: %v", got, want, err)
	}
}
func TestRebind(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`SELECT 1`:                                                `SELECT 1`,
		`SELECT a FROM t WHERE b=? AND c=?`:                       `SELECT a FROM t WHERE b=$1 AND c=$2`,
		`SELECT '?' AS q, x FROM t WHERE y=?`:                     `SELECT '?' AS q, x FROM t WHERE y=$1`,
		`SELECT 'it''s?' FROM t WHERE y=?`:                        `SELECT 'it''s?' FROM t WHERE y=$1`,
		"SELECT x -- what? \nFROM t WHERE y=?":                    "SELECT x -- what? \nFROM t WHERE y=$1",
		`SELECT $$why?$$ FROM t WHERE y=?`:                        `SELECT $$why?$$ FROM t WHERE y=$1`,
		`INSERT INTO t (a,b) VALUES (?,?) ON CONFLICT DO NOTHING`: `INSERT INTO t (a,b) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
	}
	for in, want := range cases {
		if got := database.Rebind(in); got != want {
			t.Errorf("rebind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSQLParameterBoundaries(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	var value, quoted, escaped string
	var number int64
	err := db.QueryRow(`SELECT '?' || ?::text, $$why?$$, 'it''s?', ?::bigint`, "value", int64(9007199254740993)).Scan(&value, &quoted, &escaped, &number)
	if err != nil || value != "?value" || quoted != "why?" || escaped != "it's?" || number != 9007199254740993 {
		t.Fatalf("rebound parameters: %q %q %q %d: %v", value, quoted, escaped, number, err)
	}
	var present bool
	err = db.SQL().QueryRowContext(context.Background(),
		`SELECT $1::jsonb ? 'enabled', $1::jsonb->>'message', $2::bigint`,
		`{"enabled":true,"message":"native?"}`, int64(9007199254740993)).Scan(&present, &value, &number)
	if err != nil || !present || value != "native?" || number != 9007199254740993 {
		t.Fatalf("native parameters: %v %q %d: %v", present, value, number, err)
	}
}

func TestTimestamptzAdapterPreservesStampValuesNullsAndUTCStrings(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	fixed := time.Date(2026, time.September, 15, 3, 57, 49, 123456000, time.UTC)
	written := stamp.Of(fixed)
	stringAt := fixed.Format(time.RFC3339Nano)

	var valued, stringed, closed stamp.Time
	var closedIsNull bool
	if err := db.QueryRow(`SELECT ?::timestamptz,?::timestamptz,?::timestamptz,?::timestamptz IS NULL`, written, stringAt, stamp.Time{}, stamp.Time{}).Scan(&valued, &stringed, &closed, &closedIsNull); err != nil {
		t.Fatal(err)
	}
	if !valued.Valid || !valued.Time.Equal(written.Time) || !stringed.Valid || !stringed.Time.Equal(fixed) || closed.Valid || !closedIsNull {
		t.Fatalf("valued=%v stringed=%v closed=%v is-null=%t", valued, stringed, closed, closedIsNull)
	}

	var text string
	if err := db.QueryRow(`SELECT ?::timestamptz`, stringAt).Scan(&text); err != nil || text != stringAt {
		t.Fatalf("UTC string=%q err=%v", text, err)
	}
}

func TestErrorsNameTheirStatement(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	execTest(t, db, `CREATE TABLE query_probe(id bigint)`)
	_, err := db.Exec(`UPDATE no_such_table SET x=1 WHERE id=?`, 1)
	if err == nil || !strings.Contains(err.Error(), "UPDATE no_such_table SET x=1") {
		t.Fatalf("an Exec failure must carry its statement: %v", err)
	}
	var n int
	err = db.QueryRow(`SELECT nope FROM query_probe WHERE id=?`, 1).Scan(&n)
	if err == nil || !strings.Contains(err.Error(), "SELECT nope FROM query_probe") {
		t.Fatalf("a QueryRow failure must carry its statement: %v", err)
	}
	found, err := database.Lookup(db.QueryRow(`SELECT id FROM query_probe WHERE id=?`, 424242).Scan(&n))
	if found || err != nil {
		t.Fatalf("no row is absent, not failed: %v %v", found, err)
	}
	if strings.Count(err2(db), "SELECT nope") != 1 {
		t.Fatal("the statement is attached once")
	}
}

func err2(db *database.DB) string {
	var n int
	return db.QueryRow(`SELECT nope FROM query_probe`).Scan(&n).Error()
}

func TestContextAndFaultScopesFollowEveryHandle(t *testing.T) {
	t.Parallel()
	a, b := testDB(t), testDB(t)
	execTest(t, a, `CREATE TABLE scope_probe(n int)`)
	execTest(t, b, `CREATE TABLE scope_probe(n int)`)
	native := a.SQL()
	tx, err := a.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	restore := a.FailNextStatement("INSERT INTO scope_probe")
	defer restore()
	execTest(t, b, `INSERT INTO scope_probe VALUES (1)`)
	if _, err := tx.SQL().ExecContext(context.Background(), `INSERT INTO scope_probe VALUES (1)`); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("transaction lost pool fault: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO scope_probe VALUES (2)`); err != nil {
		t.Fatalf("fault was not one-shot: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	countTest(t, a, 1, `SELECT count(*) FROM scope_probe`)
	// Every wrapper must retain the same pool-local injector. This is our
	// adapter contract, not a PostgreSQL failure-isolation test.
	for _, run := range []func() error{
		func() error { _, err := a.Exec(`SELECT 42`); return err },
		func() error {
			rows, err := a.Query(`SELECT 42`)
			if rows != nil {
				rows.Close()
			}
			return err
		},
		func() error {
			tx, err := a.Begin(context.Background())
			if err != nil {
				return err
			}
			defer tx.Rollback()
			var n int
			return tx.QueryRow(`SELECT 42`).Scan(&n)
		},
		func() error {
			tx, err := a.Begin(context.Background())
			if err != nil {
				return err
			}
			defer tx.Rollback()
			rows, err := tx.SQL().QueryContext(context.Background(), `SELECT 42`)
			if rows != nil {
				rows.Close()
			}
			return err
		},
	} {
		cleanup := a.FailNextStatement("SELECT 42")
		err := run()
		cleanup()
		if err == nil {
			t.Fatal("wrapper lost injected failure")
		}
	}
	// An older cleanup cannot clear the next installed failure.
	newer := a.FailNextStatement("INSERT INTO scope_probe")
	defer newer()
	restore()
	if _, err := native.ExecContext(context.Background(), `INSERT INTO scope_probe VALUES (3)`); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("old cleanup cleared newer fault: %v", err)
	}
	cleanup := a.FailNextStatement("SET TRANSACTION")
	defer cleanup()
	if snapshot, err := a.ReadSnapshot(context.Background()); snapshot != nil || !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("snapshot skipped checked begin: %+v %v", snapshot, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	value, err := database.Transact(ctx, a, func(tx *database.Tx) (string, error) {
		if _, err := tx.SQL().ExecContext(ctx, `INSERT INTO scope_probe VALUES (4)`); err != nil {
			return "", err
		}
		cancel()
		return "uncommitted", nil
	})
	if err == nil || value != "" {
		t.Fatalf("cancelled transaction published a result: %q %v", value, err)
	}
	countTest(t, a, 1, `SELECT count(*) FROM scope_probe`)
	if tx, err := a.Begin(ctx); tx != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled begin: %+v %v", tx, err)
	}
	if records, err := a.SQL().QueryContext(ctx, `SELECT n FROM scope_probe`); records != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query: %+v %v", records, err)
	}
}

// OpenWith makes the pool's connections with the dialer it is given (the
// test harness's own, for one): its first connection, the ping, goes
// through it, and what the dialer refuses is what Open reports.
func TestOpenWithMakesConnectionsWithItsDialer(t *testing.T) {
	t.Parallel()
	refused := errors.New("refused by the given dialer")
	var dialed []string
	_, err := database.OpenWith("postgres://forge@192.0.2.1:5432/forge?sslmode=disable", func(_ context.Context, network, addr string) (net.Conn, error) {
		dialed = append(dialed, network+" "+addr)
		return nil, refused
	})
	if !errors.Is(err, refused) || len(dialed) == 0 || dialed[0] != "tcp 192.0.2.1:5432" {
		t.Fatalf("OpenWith = %v; dialed %v", err, dialed)
	}
}

// One holder at a time: a second session asking for the same exclusive
// lock is refused at once, never kept waiting, and gets it once the first
// lets go.
func TestHoldExclusiveRefusesASecondHolder(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	const key = 0x464f524745 // any key; the database is this test's own
	release, err := db.HoldExclusive(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.HoldExclusive(t.Context(), key); !errors.Is(err, database.ErrHeldElsewhere) {
		t.Fatalf("a second holder: %v, want ErrHeldElsewhere", err)
	}
	release()
	again, err := db.HoldExclusive(t.Context(), key)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again()
}
