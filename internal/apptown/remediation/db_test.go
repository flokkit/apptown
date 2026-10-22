package remediation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/remediation/internal/remediationsql"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M)            { os.Exit(databaseSuite.Run(m)) }
func testDB(t *testing.T) *database.DB { t.Helper(); return databaseSuite.New(t) }

func completionExec(t *testing.T, db *database.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func completionCount(t *testing.T, db *database.DB, want int, q string, args ...any) {
	t.Helper()
	var got int
	if err := db.QueryRow(q, args...).Scan(&got); err != nil || got != want {
		t.Fatalf("count %d want %d: %v", got, want, err)
	}
}

// receipts is every ledger line of exactly this kind ("" for every line), read
// through the audit owner. The ledger read is bounded, so a full page cannot prove a total.
func receipts(t *testing.T, db database.Queryer, kind string) []audit.Entry {
	t.Helper()
	entries, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: kind, Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == audit.MaxLimit {
		t.Fatal("ledger page is full; it cannot prove a total")
	}
	out := entries[:0]
	for _, e := range entries {
		if kind == "" || e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func receiptCount(t *testing.T, db database.Queryer, want int, kind string) {
	t.Helper()
	if got := receipts(t, db, kind); len(got) != want {
		t.Fatalf("%s receipts %d want %d: %+v", kind, len(got), want, got)
	}
}

// jsonText reads one field of a JSON document the way ->> does: a string's
// text, any other value's JSON, and "" when the path is absent.
func jsonText(t *testing.T, raw json.RawMessage, path ...string) string {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	for _, key := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		if v, ok = m[key]; !ok {
			return ""
		}
	}
	if s, ok := v.(string); ok {
		return s
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// jsonHas is jsonb_exists: whether the document's top level has the key.
func jsonHas(t *testing.T, raw json.RawMessage, key string) bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	_, ok := m[key]
	return ok
}

// recordCounts is the owner's total of jobs, items and attempts.
func recordCounts(t *testing.T, db database.Queryer) remediationsql.RemediationRecordCountsRow {
	t.Helper()
	counts, err := queries(db).RemediationRecordCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

func readTestJob(t *testing.T, db database.Queryer, id ids.ID) Remediation {
	t.Helper()
	job, err := readJob(context.Background(), db, id)
	if err != nil {
		t.Fatal(err)
	}
	return *job
}

// everyJob is every job in the store. The fixtures hold a handful of jobs, far
// under the page; the page must not be full and must match the owner's total.
func everyJob(t *testing.T, db database.Queryer) []Remediation {
	t.Helper()
	jobs, err := readJobs(context.Background(), db, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) == 1000 || len(jobs) != int(recordCounts(t, db).Remediations) {
		t.Fatalf("job page %d does not hold every job %+v", len(jobs), recordCounts(t, db))
	}
	return jobs
}

// everyItem is every item of every job, each job's items read the way the job
// page reads them. Their number must be the owner's total, so no item is hidden.
func everyItem(t *testing.T, db database.Queryer) []Item {
	t.Helper()
	var out []Item
	for _, j := range everyJob(t, db) {
		items, err := readItems(context.Background(), db, &j.ID)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, items...)
	}
	if len(out) != int(recordCounts(t, db).Items) {
		t.Fatalf("job pages show %d items, the store holds %+v", len(out), recordCounts(t, db))
	}
	return out
}

// everyAttempt is every attempt of every job, read through the page's reader.
// That reader lists only items with an attempt number, so the total must be the
// owner's count of attempt records: an attempt the page hides fails here.
func everyAttempt(t *testing.T, db database.Queryer) []Attempt {
	t.Helper()
	var out []Attempt
	for _, j := range everyJob(t, db) {
		histories, err := readAttempts(context.Background(), db, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range histories {
			out = append(out, h...)
		}
	}
	if len(out) != int(recordCounts(t, db).Attempts) {
		t.Fatalf("job pages show %d attempts, the store holds %+v", len(out), recordCounts(t, db))
	}
	return out
}

// jobsWithStatus counts every job in that status.
func jobsWithStatus(t *testing.T, db database.Queryer, status string) int {
	t.Helper()
	return jobsWith(t, db, func(j Remediation) bool { return j.Status == status })
}

func jobsWith(t *testing.T, db database.Queryer, match func(Remediation) bool) int {
	t.Helper()
	n := 0
	for _, j := range everyJob(t, db) {
		if match(j) {
			n++
		}
	}
	return n
}

// itemsInState counts items in that state across every job.
func itemsInState(t *testing.T, db database.Queryer, state string) int {
	t.Helper()
	return itemsWith(t, db, func(i Item) bool { return i.State == state })
}

func itemsWith(t *testing.T, db database.Queryer, match func(Item) bool) int {
	t.Helper()
	n := 0
	for _, i := range everyItem(t, db) {
		if match(i) {
			n++
		}
	}
	return n
}

// attemptsWith counts the attempts in the whole store that match.
func attemptsWith(t *testing.T, db database.Queryer, match func(Attempt) bool) int {
	t.Helper()
	n := 0
	for _, a := range everyAttempt(t, db) {
		if match(a) {
			n++
		}
	}
	return n
}

// oneJob requires exactly one job in the store to match, and that it is job id.
func oneJob(t *testing.T, db database.Queryer, id ids.ID, match func(Remediation) bool) {
	t.Helper()
	if n, job := jobsWith(t, db, match), readTestJob(t, db, id); n != 1 || !match(job) {
		t.Fatalf("%d matching jobs; job %d: %+v", n, id, job)
	}
}

// oneItem requires exactly one item in the store to match, and that it is item id.
func oneItem(t *testing.T, db database.Queryer, id ids.ID, match func(Item) bool) {
	t.Helper()
	current, err := readItem(context.Background(), db, id)
	if err != nil {
		t.Fatal(err)
	}
	if n := itemsWith(t, db, match); n != 1 || !match(current) {
		t.Fatalf("%d matching items; item %d: %+v", n, id, current)
	}
}

func createTestProject(t *testing.T, db *database.DB, slug, name, branch string) ids.ID {
	t.Helper()
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: name})
	})
	if err != nil {
		t.Fatal(err)
	}
	if branch != "" {
		if err := project.New(db, nil).SetDefaultBranch(context.Background(), id, branch); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// readOnly supplies an unauthorized Work and no resolver; no pass runs.
// Reads exercise the service without admitting external work.
func readOnly(t *testing.T, db *database.DB) *Service {
	t.Helper()
	return New(db, Dependencies{Work: workflow.New(context.Background(), db, workflow.Dependencies{}), Caller: context.Background()})
}

// Real round-trip instrumentation retains the pool's fault scope.
type countedQueries struct {
	database.Handle
	reads, writes int
}

func (d *countedQueries) SQL() database.Handle { return d.Handle.WithExecutor(d) }
func (d *countedQueries) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	d.writes++
	return d.DBTX.ExecContext(ctx, q, args...)
}
func (d *countedQueries) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	d.reads++
	return d.DBTX.QueryContext(ctx, q, args...)
}
func (d *countedQueries) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	d.reads++
	return d.DBTX.QueryRowContext(ctx, q, args...)
}

// race runs n copies of f started on one signal and returns their results.
func race(n int, f func(i int) int) []int {
	out := make([]int, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			out[i] = f(i)
		}(i)
	}
	close(start)
	wg.Wait()
	return out
}

// outcomeSnapshot is every remediation record and receipt, for asserting that
// a failed operation changed nothing at all.
func outcomeSnapshot(t *testing.T, db *database.DB) string {
	t.Helper()
	var snapshot string
	// sql-guarantee: rollback proof — every stored column of every job, item, attempt and receipt, so no hidden row or field a reader omits can change unnoticed.
	if err := db.QueryRow(`SELECT jsonb_build_object(
		'items',(SELECT jsonb_agg(i ORDER BY id) FROM remediation_items i),
		'attempts',(SELECT jsonb_agg(a ORDER BY id) FROM remediation_attempts a),
		'jobs',(SELECT jsonb_agg(j ORDER BY id) FROM remediations j),
		'audit',(SELECT jsonb_agg(a ORDER BY id) FROM audit_log a))::text`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
