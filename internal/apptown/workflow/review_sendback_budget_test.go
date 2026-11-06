package workflow

import (
	"context"
	"testing"
)

// Publication repair must leave all three failed-check rounds available.
func TestReviewPublicationRepairDoesNotSpendCheckRounds(t *testing.T) {
	t.Parallel()
	s, run, agent, review, _ := retryStageFixture(t)
	// Seed prior receipts as in the round-limit regression: only their history
	// is needed to test admission of the next failed-check repair.
	insertRunEventForTest(t, s.db, run.ID, "step.rework", []byte(`{"key":"publication-not-forward","triggerStep":"`+agent.String()+`"}`))
	for i := 0; i < 2; i++ {
		insertRunEventForTest(t, s.db, run.ID, "step.rework", []byte(`{}`))
	}
	result, err := (reworkStore{s.db, clocked{}}).fail(context.Background(), reworkReviewClaim(t, s.db, review), StepOutcome{Status: StatusFail, Evidence: []byte(`{"reason":"third check repair"}`)})
	if err != nil || result.agentID != agent {
		t.Fatalf("publication recovery consumed the third failed-check round: agent=%s want=%s err=%v", result.agentID, agent, err)
	}
}
