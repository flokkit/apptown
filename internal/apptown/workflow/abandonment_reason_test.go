package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/ids"
)

func TestAbandonmentReasonsProjectTheRecordedFailure(t *testing.T) {
	t.Parallel()
	s, in := invocationFixture(t)
	const reason = "cloning workspace: disk unavailable"
	s.starter.runtime.Prepare = func(context.Context, *Run) error { return errors.New(reason) }
	ch, run, err := s.CreateAndRun(t.Context(), in)
	if err == nil || ch == nil || run == nil {
		t.Fatalf("failed preparation: %+v %+v %v", ch, run, err)
	}
	stored, err := s.Change(t.Context(), ch.ID)
	if err != nil || stored.AbandonReason != reason {
		t.Fatalf("change reason: %+v %v", stored, err)
	}
	snapshot, err := s.Snapshot(t.Context(), run.ID, DetailView)
	if err != nil || snapshot.State != ActivityAbandoned || snapshot.StateDetail != reason {
		t.Fatalf("run reason: %+v %v", snapshot, err)
	}
	// The batched projection used when reading a workflow tree agrees with detail.
	graph, err := readWorkflowActivity(t.Context(), s.db, []ids.ID{run.ID})
	if err != nil {
		t.Fatal(err)
	}
	activity, err := graph.classify(run.ID, nil, map[ids.ID]bool{})
	if err != nil || activity.State != ActivityAbandoned || activity.Detail != reason {
		t.Fatalf("activity: %+v %v", activity, err)
	}
}
