package scm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm/internal/scmsql"
	"flokkit.io/forge/internal/ids"
)

var errFixtureClosed = errors.New("fixture work is closed")

type fixtureClaim struct {
	source   Source
	changeID ids.ID
	revision int
	closed   bool
}

func (c *fixtureClaim) Source() Source { return c.source }
func (c *fixtureClaim) CheckIn(ctx context.Context, tx *database.Tx, purpose Purpose) (Refusal, error) {
	var id ids.ID
	// sql-guarantee: the fixture claim takes the same project row lock a real claim takes, so check-in serializes as in production; a lock, not an assertion read
	if err := tx.SQL().QueryRowContext(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, c.source.ProjectID).Scan(&id); err != nil {
		return Refusal{}, err
	}
	revision, closed, err := readFixtureWork(ctx, tx.SQL(), c.source.RunID, true)
	if err != nil {
		return Refusal{}, err
	}
	if revision != c.revision || closed != c.closed {
		return Refusal{}, ErrChanged
	}
	if purpose == NewEffect && closed {
		return Refusal{}, errFixtureClosed
	}
	return Refusal{}, nil
}
func (c *fixtureClaim) RecordIn(ctx context.Context, tx *database.Tx, r Receipt) error {
	kind, err := r.Event()
	if err != nil {
		return err
	}
	if r.Kind == Completed {
		// sql-guarantee: this fake external Work participant changes its owned Change in SCM's settlement transaction; Workflow imports SCM and cannot be used here.
		if err := requireFixtureWrite(tx.SQL().ExecContext(ctx, `UPDATE changes SET landed_sha=$1 WHERE id=$2`, r.SHA, c.changeID)); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	// sql-guarantee: this fake external Work participant records its receipt inside SCM's transaction; Workflow imports SCM and cannot be used here.
	return requireFixtureWrite(tx.SQL().ExecContext(ctx, `INSERT INTO run_events(id,run_id,kind,payload_json,at) VALUES ($1,$2,$3,$4,'2026-10-09T00:00:00Z')`, ids.New(time.Now()), c.source.RunID, kind, string(raw)))
}
func mergeFixture(t *testing.T) (mergeStore, *fixtureClaim) {
	t.Helper()
	db := testDB(t)
	projectID, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "scm", Name: "SCM"})
	})
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: create the sparse Change owned by SCM's fake Work participant; importing Workflow's creator would form the production Workflow-to-SCM cycle.
	change := completionInsert(t, db, `INSERT INTO changes(id,number,project_id,type,title,created_at) VALUES (?,?,?,'feature','Fixture','2026-10-09T00:00:00Z')`, fixtureNumber(), projectID)
	// sql-guarantee: create the sparse Run identity consumed by SCM's fake Work participant; importing Workflow admission would form the production Workflow-to-SCM cycle.
	run := completionInsert(t, db, `INSERT INTO runs(id,number,project_id,change_id,workflow_name,workflow_version,kind,current_stage,definition_json,created_at,updated_at) VALUES (?,?,?,?,'fixture',1,'pipeline','finish','{}','2026-10-09T00:00:00Z','2026-10-09T00:00:00Z')`, fixtureNumber(), projectID, change)
	completionExec(t, db, `CREATE TABLE fixture_work(run_id uuid PRIMARY KEY,revision int NOT NULL DEFAULT 1,closed boolean NOT NULL DEFAULT false)`)
	// sql-guarantee: seed the test-only external participant record whose revision and closure SCM must recheck atomically.
	completionExec(t, db, `INSERT INTO fixture_work(run_id) VALUES (?)`, run)
	// A real hub with one commit on main: settlement reads main back from it.
	return mergeStore{db: db}, &fixtureClaim{source: Source{RunID: run, ProjectID: projectID, Repository: newMainHub(t), Workspace: "fixture-workspace", Branch: "work", DefaultBranch: "main", Candidate: "fixture-candidate", Target: "fixture-target", PinRequired: true}, changeID: change, revision: 1}
}
func refreshedClaim(t *testing.T, p *preparedMerge) *fixtureClaim {
	t.Helper()
	c := *p.work.(*fixtureClaim)
	var err error
	if c.revision, c.closed, err = readFixtureWork(context.Background(), p.db.SQL(), c.source.RunID, false); err != nil {
		t.Fatal(err)
	}
	return &c
}

// The fake Work participant stands in for Workflow, which owns changes and
// run_events and imports SCM. These are the fake's own reads of what it
// recorded, so each assertion names a fact instead of carrying its own query.

type fixtureRowReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readFixtureWork(ctx context.Context, q fixtureRowReader, runID ids.ID, forUpdate bool) (revision int, closed bool, err error) {
	// sql-guarantee: fixture_work is the fake participant's own test-only table; this is its only read.
	query := `SELECT revision,closed FROM fixture_work WHERE run_id=$1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	err = q.QueryRowContext(ctx, query, runID).Scan(&revision, &closed)
	return revision, closed, err
}

// fixtureReceipts is every receipt the fake participant recorded of kind
// ("" for all), with its decoded payload.
func fixtureReceipts(t *testing.T, db *database.DB, kind string) []map[string]any {
	t.Helper()
	// sql-guarantee: run_events belongs to Workflow, which imports SCM; the fake participant that wrote these rows reads them back here.
	rows, err := db.SQL().QueryContext(context.Background(), `SELECT payload_json FROM run_events WHERE $1='' OR kind=$1 ORDER BY id`, kind)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		payload := map[string]any{}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, payload)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func wantFixtureReceipts(t *testing.T, db *database.DB, kind string, want int) {
	t.Helper()
	if got := len(fixtureReceipts(t, db, kind)); got != want {
		t.Fatalf("%q receipts: %d want %d", kind, got, want)
	}
}

// fixtureChangeSHA is the source the fake participant recorded on its Change.
func fixtureChangeSHA(t *testing.T, db *database.DB, changeID ids.ID) string {
	t.Helper()
	var sha string
	// sql-guarantee: changes belongs to Workflow, which imports SCM; the fake participant that wrote landed_sha reads it back here.
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT landed_sha FROM changes WHERE id=$1`, changeID).Scan(&sha); err != nil {
		t.Fatal(err)
	}
	return sha
}

// storedMerge reports whether SCM holds a merge intent for the run, through
// its own generated read.
func storedMerge(t *testing.T, db *database.DB, runID ids.ID) (scmsql.RunMerge, bool) {
	t.Helper()
	m, err := scmsql.New(db.SQL()).MergeByRun(context.Background(), runID)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return m, true
}

func wantStoredMerge(t *testing.T, db *database.DB, runID ids.ID, want bool) {
	t.Helper()
	if _, got := storedMerge(t, db, runID); got != want {
		t.Fatalf("merge intent for run %d stored=%v want %v", runID, got, want)
	}
}
func race(n int, fn func(int) int) []int {
	results := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; results[i] = fn(i) }()
	}
	close(start)
	wg.Wait()
	return results
}
func count(values []int, want int) int {
	n := 0
	for _, v := range values {
		if v == want {
			n++
		}
	}
	return n
}

func requireFixtureWrite(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	return requireWrite(result.RowsAffected())
}
