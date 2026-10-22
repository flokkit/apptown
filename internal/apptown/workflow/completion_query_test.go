package workflow

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
)

func TestCompletionParentObservationIsOneRead(t *testing.T) {
	t.Parallel()
	store, run, _, _ := retryFixture(t)
	counted := &countedQueries{Handle: store.db.SQL()}
	parent, err := workflowsql.New(counted.SQL()).CompletedParent(context.Background(), workflowsql.CompletedParentParams{ParentRunID: run.ID})
	if err != nil || counted.reads != 1 || parent.Run.ID != run.ID || parent.CurrentCall {
		t.Fatalf("parent snapshot: %+v statements=%d error=%v", parent, counted.reads, err)
	}
}
