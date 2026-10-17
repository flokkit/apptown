package workflow

import (
	"context"
	"fmt"
	"testing"

	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

func TestWorkflowReworkHistoryIsOneCurrentRead(t *testing.T) {
	t.Parallel()
	store, run, agent, source := retryFixture(t)
	trigger := insertRunEventForTest(t, store.db, run.ID, "step.finished", []byte(`{"step":"review"}`))
	checkStart := insertRunEventForTest(t, store.db, run.ID, "step.started", []byte(`{"step":"review"}`))
	agentStart := insertRunEventForTest(t, store.db, run.ID, "step.started", []byte(`{"step":"build"}`))
	counted := &countedQueries{Handle: store.db.SQL()}
	query := workflowsql.New(counted.SQL())
	params := workflowsql.ReworkHistoryParams{RunID: run.ID, SourceID: source, TriggerKind: "step.finished", SourceName: "review", AgentName: "build"}
	history, err := query.ReworkHistory(context.Background(), params)
	if err != nil || counted.reads != 1 || history.TriggerEvent != trigger || history.CheckStart != checkStart || history.AgentStart != agentStart || history.Seen || history.Rounds != 0 {
		t.Fatalf("rework history: %+v reads=%d err=%v", history, counted.reads, err)
	}
	insertRunEventForTest(t, store.db, run.ID, "step.rework", []byte(fmt.Sprintf(`{"triggerStep":%q,"triggerEvent":%q,"agentStep":%q}`, source, trigger, agent)))
	history, err = query.ReworkHistory(context.Background(), params)
	if err != nil || counted.reads != 2 || !history.Seen || history.Rounds != 1 {
		t.Fatalf("history did not see committed trigger: %+v reads=%d err=%v", history, counted.reads, err)
	}
}
