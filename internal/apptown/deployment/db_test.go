package deployment

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

// The package's rules are tested against the canonical schema, each test
// on its own clone of one template database (internal/dbtest).
var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M) {
	code := databaseSuite.Run(m)
	gittest.RemoveTemplates()
	os.Exit(code)
}
func testDB(t *testing.T) *database.DB { t.Helper(); return databaseSuite.New(t) }

func mustInsert(t *testing.T, db *database.DB, q string, args ...any) ids.ID {
	t.Helper()
	// The query's first column and placeholder are the new row's id.
	id := ids.New(time.Now())
	if _, err := db.Exec(q, append([]any{id}, args...)...); err != nil {
		t.Fatal(err)
	}
	return id
}

func mustExec(t *testing.T, db *database.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// race runs f n times at once and returns what each run answered.
func race(n int, f func(i int) int) []int {
	out := make([]int, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
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

func count(codes []int, want int) int {
	n := 0
	for _, c := range codes {
		if c == want {
			n++
		}
	}
	return n
}

func createTestProject(t *testing.T, db *database.DB, slug, name string) ids.ID {
	t.Helper()
	return createProjectFor(t, db, slug, name, project.Ordinary)
}

// createTestCopy is a project made as an eval copy, as CreateCopy makes one.
func createTestCopy(t *testing.T, db *database.DB, slug, name string) ids.ID {
	t.Helper()
	return createProjectFor(t, db, slug, name, project.EvalCopy)
}

func createProjectFor(t *testing.T, db *database.DB, slug, name string, purpose project.Purpose) ids.ID {
	t.Helper()
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: name, Purpose: purpose})
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Stored projections can have an unbound or opaque target identity.
func storedDeploymentFixture(t *testing.T, db *database.DB, p deploymentsql.EnsureStandingParams) ids.ID {
	t.Helper()
	if p.Kind == "" {
		p.Kind = Standing.String()
	}
	if p.Feed == "" {
		p.Feed = "landing"
	}
	if p.Audience == "" {
		p.Audience = "team"
	}
	if p.DataClass == "" {
		p.DataClass = "synthetic"
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now().UTC()
	}
	row, err := deploymentsql.New(db.SQL()).EnsureStanding(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return row.ID
}
func storedBundleFixture(t *testing.T, db *database.DB, p deploymentsql.InsertBundleParams) ids.ID {
	t.Helper()
	if p.PartsJson == nil {
		p.PartsJson = []byte(`{}`)
	}
	if p.InitJson == nil {
		p.InitJson = []byte(`{}`)
	}
	if p.BuiltFromJson == nil {
		p.BuiltFromJson = []byte(`{}`)
	}
	if p.BuiltOnJson == nil {
		p.BuiltOnJson = []byte(`{}`)
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	row, err := deploymentsql.New(db.SQL()).InsertBundle(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return row.ID
}

// Preserve all unselected fields, including the timestamp, in fence probes.
func mutateStoredDeployment(t *testing.T, db *database.DB, projectID ids.ID, name string, mutate func(*deploymentsql.Deployment)) {
	t.Helper()
	q := deploymentsql.New(db.SQL())
	r, err := q.DeploymentByName(context.Background(), deploymentsql.DeploymentByNameParams{ProjectID: projectID, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	mutate(&r)
	if _, err := q.EnsureStanding(context.Background(), deploymentsql.EnsureStandingParams{ID: ids.New(r.UpdatedAt), ProjectID: r.ProjectID, Name: r.Name, Kind: r.Kind, TargetID: r.TargetID, Feed: r.Feed, Audience: r.Audience, DataClass: r.DataClass, UpdatedAt: r.UpdatedAt}); err != nil {
		t.Fatal(err)
	}
	if n, err := q.AdmitGeneration(context.Background(), deploymentsql.AdmitGenerationParams{ID: r.ID, DesiredGeneration: r.DesiredGeneration, Readiness: r.Readiness, UpdatedAt: r.UpdatedAt}); err != nil || n != 1 {
		t.Fatalf("mutate generation: rows=%d err=%v", n, err)
	}
}

func cutBundleFixture(t *testing.T, db *database.DB, projectID ids.ID, revision string, origin BundleOrigin) ids.ID {
	t.Helper()
	id, err := NewBundles(db).Cut(t.Context(), projectID, revision, nil, nil, nil, origin)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func shippedChangeFixture(t *testing.T, db *database.DB, projectID ids.ID) ids.ID {
	t.Helper()
	return shippedChangeAt(t, db, projectID, time.Now())
}

// shippedChangeAt ships a Change through Work, closed at AppTown's time at.
func shippedChangeAt(t *testing.T, db *database.DB, projectID ids.ID, at time.Time) ids.ID {
	t.Helper()
	c, err := workflow.NewCreator(db, func(context.Context, workflow.CreatedChange) error { return nil }).Create(context.Background(), workflow.ChangeDraft{ProjectID: projectID, Type: "feature", Title: "Included"}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		_, err := workflow.TransitionChangeIn(context.Background(), tx, projectID, c.ID, workflow.ChangeMove{Action: workflow.ShipChange}, at)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// fixtureNumbers gives fixture rows a number within their project that no
// other fixture row has: numbers are unique per project, and the counter
// is shared by every test in the package.
var fixtureNumbers atomic.Int64

func fixtureNumber() int64 { return 1000 + fixtureNumbers.Add(1) }

// releaseGit runs git in a test's repository.
func releaseGit(ctx context.Context, repo string, args ...string) (string, error) {
	return gitexec.Run(ctx, repo, args...)
}
