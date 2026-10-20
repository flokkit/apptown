package workflowexec

import (
	"context"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/tasks"
	"flokkit.io/forge/internal/forge/workflow"
)

// startLost is a backend whose task stays created and whose start reply is
// lost, and AppTown's context ends as it is: the runner is then in its
// pause before observing again. Nothing else of a driver is used.
type startLost struct {
	execution.TaskDriver
	cancel context.CancelFunc
}

func (startLost) Observe(context.Context, execution.TaskRef) (execution.TaskObservation, error) {
	return execution.TaskObservation{State: execution.TaskCreated}, nil
}

func (d startLost) Start(context.Context, execution.TaskRef) error {
	d.cancel()
	return errors.New("the start's reply was lost")
}

// A task whose start reply was lost is observed again after a pause; the
// pause ends with the context, which the runner reports.
func TestAStartsRetryPauseEndsWithItsContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := &TaskRunner{Driver: startLost{cancel: cancel}, Poll: time.Hour}
	var attempt tasks.Attempt
	attempt.Operation, attempt.Deadline = "op-1", time.Now().Add(time.Hour)
	attempt.Admitted.Expires = time.Now().Add(time.Hour)
	done := make(chan error, 1)
	go func() {
		_, err := r.begin(ctx, attempt)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a begin whose context ended answered %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the pause before observing again outlived its context")
	}
}

// A review waits for its stage's boot to settle; the wait ends with the
// review's context, answering the boot as last read.
func TestAReviewsWaitForItsBootEndsWithItsContext(t *testing.T) {
	t.Parallel()
	var executor *Executor
	_, ctx, stepIDs, _, _ := reviewFixtureWith(t, true, func(execution.Request) execution.Outcome {
		t.Error("nothing is invoked")
		return execution.Outcome{}
	}, func(e *Executor) { executor = e })
	boot, err := workflow.ReadStepIn(ctx, executor.db, stepIDs["boot"])
	if err != nil {
		t.Fatal(err)
	}
	if boot.Status != workflow.StatusPending {
		t.Fatalf("the boot is %s; the test needs it unsettled", boot.Status)
	}
	waiting, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	done := make(chan *workflow.Step, 1)
	go func() { done <- executor.awaitStageBoot(waiting, boot.RunID, boot.Stage) }()
	select {
	case got := <-done:
		if got == nil || got.ID != boot.ID || got.Status != workflow.StatusPending {
			t.Fatalf("the wait answered %+v; want the boot as last read", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the wait for the stage's boot outlived its context")
	}
}
