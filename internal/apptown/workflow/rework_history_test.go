package workflow

import (
	"context"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
)

func TestWorkflowReworkHistoryIsOneCurrentRead(t *testing.T) {
	t.Parallel()
	store, run, agent, source := retryFixture(t)
	trigger := insertRunEventForTest(t, store.db, run.ID, "step.finished", []byte(`{"step":"review"}`))
	checkStart := insertRunEventForTest(t, store.db, run.ID, "step.started", []byte(`{"step":"review"}`))
	agentStart := insertRunEventForTest(t, store.db, run.ID, "step.started", []byte(`{"step":"build"}`))
	counted := &countedQueries{Handle: store.db.SQL()}
	query := workflowsql.New(counted.SQL())
	params := workflowsql.ReworkHistoryParams{RunID: run.ID, SourceID: source, TriggerKind: "step.finished", SourceName: "review", AgentName: "build", ReworkKey: "publication-not-forward"}
	history, err := query.ReworkHistory(context.Background(), params)
	if err != nil || counted.reads != 1 || history.TriggerEvent != trigger || history.CheckStart != checkStart || history.AgentStart != agentStart || history.Seen || history.Rounds != 0 {
		t.Fatalf("rework history: %+v reads=%d err=%v", history, counted.reads, err)
	}
	insertRunEventForTest(t, store.db, run.ID, "step.rework", []byte(fmt.Sprintf(`{"triggerStep":%q,"triggerEvent":%q,"agentStep":%q}`, source, trigger, agent)))
	history, err = query.ReworkHistory(context.Background(), params)
	if err != nil || counted.reads != 2 || !history.Seen || history.Rounds != 1 || history.KeyRounds != 0 {
		t.Fatalf("history did not see committed trigger: %+v reads=%d err=%v", history, counted.reads, err)
	}
	insertRunEventForTest(t, store.db, run.ID, "step.rework", []byte(fmt.Sprintf(`{"key":"publication-not-forward","triggerStep":%q}`, source)))
	// Another source's recovery must not consume this step's allowance.
	insertRunEventForTest(t, store.db, run.ID, "step.rework", []byte(fmt.Sprintf(`{"key":"publication-not-forward","triggerStep":%q}`, agent)))
	history, err = query.ReworkHistory(context.Background(), params)
	if err != nil || history.KeyRounds != 1 || history.Rounds != 1 {
		t.Fatalf("separate recovery allowance: %+v err=%v", history, err)
	}
	insertRunEventForTest(t, store.db, run.ID, "step.retried", []byte(`{}`))
	history, err = query.ReworkHistory(context.Background(), params)
	if err != nil || history.KeyRounds != 0 || history.Rounds != 0 {
		t.Fatalf("person retry did not reset allowance: %+v err=%v", history, err)
	}
}
