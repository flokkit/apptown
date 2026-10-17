package deployment

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/deployment/internal/releasesql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

type releaseQueryCounter struct {
	database.Handle
	reads int
}

func (d *releaseQueryCounter) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	d.reads++
	return d.DBTX.QueryRowContext(ctx, q, args...)
}

// The metadata selection and insertion stay one database statement.
func TestWorkflowCompletionMetadataStatements(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	project := createTestProject(t, db, "metadata", "Metadata")
	run := idstest.N(17) // opaque Work identity supplied to the metadata participant
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockReleaseProjectIn(tx, project); err != nil {
		t.Fatal(err)
	}
	counted := &releaseQueryCounter{Handle: tx.SQL()}
	release, err := releasesql.New(counted.Handle.WithExecutor(counted)).CreateRelease(context.Background(), releasesql.CreateReleaseParams{ID: ids.New(time.Now()), FromWorkflow: true, State: ReleaseFromWorkflow.InitialState().String(), Deployment: "main", ProjectID: project, RunID: run, At: time.Now()})
	if err != nil || counted.reads != 1 || release.ID.IsZero() || release.Number != 1 {
		t.Fatalf("release metadata and insertion: %+v statements=%d error=%v", release, counted.reads, err)
	}
}
