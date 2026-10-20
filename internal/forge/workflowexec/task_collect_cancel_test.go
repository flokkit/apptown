package workflowexec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/forge/tasks"
	"flokkit.io/forge/internal/forge/workflow"
)

// A step whose context ends while its exited task's result is collected
// (AppTown stopping with a bundle in flight) has not lost the result:
// the attempt is never held as "result not kept", and a restarted AppTown
// collects and publishes it without running the task again.
//
// Kills: a stopped collection read as a result that could not be kept.
func TestAStoppedCollectionLeavesTheResultCollectible(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	stopped, stop := context.WithCancel(a.ctx)
	defer stop()
	state := resultWorker(t, a, fake, func(n int, w http.ResponseWriter, reply *httptest.ResponseRecorder) {
		if n == 1 {
			stop() // AppTown stops while the result is in flight
		}
		pass(w, reply)
	})
	executed := make(chan error, 1)
	go func() { executed <- a.work.ExecuteStep(stopped, step.ID) }()
	key := taskKeyOf(step)
	awaitStarted(t, fake, key, "the task never started on the worker")
	writeRemote(t, filepath.Join(state, "workspaces", key), map[string]string{"only-copy.txt": "keep this work\n"})
	fake.Exit(key, 0)
	<-executed
	attempt, err := tasks.New(a.db).Get(context.Background(), key)
	if err != nil || attempt.Observed == tasks.Held {
		t.Fatalf("a stopped collection held the result: %+v %v", attempt, err)
	}
	a.work.Stop()
	b := restarted(t, a)
	awaitStep(t, b, step.ID, workflow.StatusPass)
	if starts := fake.Starts(key); starts != 1 {
		t.Fatalf("the task started %d times", starts)
	}
}
