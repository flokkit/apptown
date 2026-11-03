package workflow

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/ids"
)

// Two callers racing the same acceptance check commit exactly one change.
// The losing caller names the committed change, before workspace effects.
func TestRemediationCreationRefusesConcurrentCoverage(t *testing.T) {
	t.Parallel()
	c, draft := creationFixture(t)
	draft.Type = "remediation"
	draft.Acceptance = &Acceptance{Checks: []AcceptanceCheck{{Block: "hygiene", Version: 1, Check: "clean"}}}
	type result struct {
		created CreatedChange
		err     error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			made, err := c.records.create(context.Background(), draft, "alice", nil)
			results <- result{made, err}
		}()
	}
	close(start)
	var winner *Change
	var refusal error
	for range 2 {
		r := <-results
		if r.err != nil {
			if refusal != nil {
				t.Fatalf("both starts refused: %v; %v", refusal, r.err)
			}
			refusal = r.err
		} else {
			if winner != nil {
				t.Fatal("both starts created changes")
			}
			winner = r.created.Change
		}
	}
	if winner == nil || refusal == nil || !strings.Contains(refusal.Error(), fmt.Sprintf("being fixed in change #%d", winner.Number)) {
		t.Fatalf("winner %+v, refusal %v", winner, refusal)
	}
	// sql-guarantee: independent persistence count proves a refused race left exactly one change.
	completionCount(t, c.records.db, 1, `SELECT count(*) FROM changes`)
	if _, err := c.records.create(context.Background(), draft, "alice", nil); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("change #%d", winner.Number)) {
		t.Fatalf("second start: %v", err)
	}
	coverage, err := OpenRemediationsIn(context.Background(), c.records.db, []ids.ID{draft.ProjectID})
	if err != nil || len(coverage) != 1 || coverage[0].ChangeID != winner.ID {
		t.Fatalf("coverage: %+v %v", coverage, err)
	}
	_, err = NewAbandonments(c.records.db, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{}).Abandon(context.Background(), Abandonment{Who: "alice", ChangeID: winner.ID})
	if err != nil {
		t.Fatal(err)
	}
	coverage, err = OpenRemediationsIn(context.Background(), c.records.db, []ids.ID{draft.ProjectID})
	if err != nil || len(coverage) != 0 {
		t.Fatalf("abandoned coverage: %+v %v", coverage, err)
	}
	if _, err := c.records.create(context.Background(), draft, "alice", nil); err != nil {
		t.Fatalf("start after abandonment: %v", err)
	}
}
