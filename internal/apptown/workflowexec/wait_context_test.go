package workflowexec

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/execution"
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
