package deployment

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A zero identity or empty name is still an exact filter. Consolidating the
// report must not turn an explicit empty scope into an instance-wide read.
func TestRecordReportsKeepExplicitEmptyScopes(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := createTestProject(t, db, "scope", "Scope")
	storedDeploymentFixture(t, db, deploymentsql.EnsureStandingParams{ID: ids.New(time.Now()), ProjectID: pid, Name: "main", TargetID: idstest.N(7)})
	zero := storedDeploymentFixture(t, db, deploymentsql.EnsureStandingParams{ID: ids.New(time.Now()), ProjectID: pid, Name: "unbound"})
	store := NewDeployments(db)
	if got, err := store.OnTarget(t.Context(), ids.Nil); err != nil || len(got) != 1 || got[0].ID != zero {
		t.Fatalf("zero target: %+v %v", got, err)
	}
	if got, err := store.Named(t.Context(), ""); err != nil || len(got) != 0 {
		t.Fatalf("empty name: %+v %v", got, err)
	}
	if got, err := store.All(t.Context()); err != nil || len(got) != 2 {
		t.Fatalf("all deployments: %+v %v", got, err)
	}
	if _, err := deploymentsql.New(db.SQL()).InsertRetainedResource(context.Background(), deploymentsql.InsertRetainedResourceParams{ID: ids.New(time.Now()), ProjectID: pid, Deployment: "main", TargetID: idstest.N(7), Need: "db", HandleJson: []byte(`{}`), RetainedBy: "alice", RetainedAt: time.Now().UTC(), Disposition: RetainedOpen.String()}); err != nil {
		t.Fatal(err)
	}
	retained := NewRetained(db)
	if got, err := retained.ForProject(context.Background(), ids.Nil); err != nil || len(got) != 0 {
		t.Fatalf("zero project: %+v %v", got, err)
	}
	if got, err := retained.openForTarget(context.Background(), ids.Nil); err != nil || len(got) != 0 {
		t.Fatalf("zero retained target: %+v %v", got, err)
	}
	if got, err := retained.All(context.Background()); err != nil || len(got) != 1 {
		t.Fatalf("all retained: %+v %v", got, err)
	}
}
