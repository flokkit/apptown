package workflow

import (
	"context"
	"testing"

	"flokkit.io/forge/internal/forge/schema"
)

// A dispatched step runs under Work's lifetime: when AppTown ends it (at
// shutdown, after its grace), the step's context ends too, rather than the
// step running on unbounded.
//
// Kills: a dispatched runner given a context of its own.
func TestADispatchedStepStopsWithWorksLifetime(t *testing.T) {
	t.Parallel()
	q, run, id := admitSingleStep(t, schema.Step{Name: "accept", Uses: "script"})
	life, end := context.WithCancel(context.Background())
	defer end()
	var stopped bool
	s := New(life, q.db, Dependencies{Automatic: []string{"script"}, Runners: map[string]Runner{"script": func(e *Execution) {
		end() // AppTown ends Work's lifetime while the step runs
		stopped = e.Context().Err() != nil
	}}})
	s.authority = allowProject(run.ProjectID)
	s.dispatcher.runners["script"](id)
	if !stopped {
		t.Fatal("the step's context outlived Work's lifetime")
	}
}
