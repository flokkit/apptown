package conformance

import (
	"context"
	"errors"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance/internal/conformancesql"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/stamp"

	"github.com/flokkit/apptown/internal/database"

	"strconv"

	"testing"
)

func TestDispositionReadsAreScopedCheckedAndBatched(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	projects := []ids.ID{}
	for i := 0; i < 12; i++ {
		id := createTestProject(t, db, "exception-"+strconv.Itoa(i), "Exceptions", "")
		projects = append(projects, id)
		createDispositionFixture(t, db, ExceptionRequest{ProjectID: id, Identifier: "live", Kind: AcceptedRisk, Justification: "test"})
		createDispositionFixture(t, db, ExceptionRequest{ProjectID: id, Identifier: "expired", Kind: FalsePositive, Justification: "test", ExpiresAt: stamp.Of(time.Now().Add(-time.Second))})
	}
	for _, scope := range [][]ids.ID{nil, {}, projects[:1], projects} {
		counted := &countedQueries{Handle: db.SQL()}
		active, err := readDispositions(context.Background(), counted, scope, true, time.Now())
		if err != nil || active == nil || len(active) != len(scope) || counted.reads != 1 {
			t.Fatalf("active rows=%d scope=%d reads=%d: %v", len(active), len(scope), counted.reads, err)
		}
		all, err := readDispositions(context.Background(), db, scope, false, time.Now())
		if err != nil || len(all) != len(scope)*2 {
			t.Fatalf("all rows=%d: %v", len(all), err)
		}
	}
	dbtest.FailNextStatement(t, db, "FROM dispositions")
	if rows, err := readDispositions(context.Background(), db, projects, true, time.Now()); rows != nil || !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("failed read returned rows: %+v %v", rows, err)
	}
	if _, err := queries(db).InsertDisposition(context.Background(), conformancesql.InsertDispositionParams{ID: ids.New(stamp.Of(time.Now()).Time), ProjectID: projects[len(projects)-1], Identifier: "corrupt", Status: "invalid-kind", Justification: "test", By: "reviewer", CreatedAt: stamp.Of(time.Now()).Time}); err != nil {
		t.Fatal(err)
	}
	if rows, err := readDispositions(context.Background(), db, projects, true, time.Now()); rows != nil || !errors.Is(err, ErrDispositionKind) {
		t.Fatalf("corrupt row returned partial evidence: %+v %v", rows, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if rows, err := readDispositions(ctx, db, projects, true, time.Now()); rows != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %+v %v", rows, err)
	}
}
