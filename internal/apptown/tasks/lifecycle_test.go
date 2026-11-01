package tasks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/flokkit/apptown/internal/ids"
)

// invocation reserves another invocation of the fixture's step attempt,
// named name, in workspace.
func invocation(t *testing.T, r *Records, in Admission, name, workspace string) Admission {
	t.Helper()
	a := in
	a.Operation, a.Invocation, a.Workspace = in.Operation+"-"+name, name, workspace
	if _, err := r.Reserve(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return a
}

// reach brings a reserved attempt to state through the owner's transitions.
func reach(t *testing.T, r *Records, operation string, state Observed) {
	t.Helper()
	ctx := context.Background()
	var err error
	switch state {
	case Reserved:
	case Started:
		err = r.Start(ctx, operation)
	case Collecting:
		err = r.Exited(ctx, operation, 3)
	case Unknown:
		err = r.RecordUnknown(ctx, operation, "lost")
	case Refused:
		err = r.Refuse(ctx, operation, "refused")
	case Held:
		err = r.Hold(ctx, operation, "not kept")
	case Settled:
		err = r.Settle(ctx, operation, nil, "")
	default:
		t.Fatalf("no way to reach %s", state)
	}
	if err != nil {
		t.Fatalf("reaching %s: %v", state, err)
	}
}

// The states the table's declaration allows (schema.sql).
func declaredStates(t *testing.T) []Observed {
	t.Helper()
	ddl, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	list := regexp.MustCompile(`observed IN \(([^)]*)\)`).FindSubmatch(ddl)
	if list == nil {
		t.Fatal("schema.sql declares no observed states")
	}
	var states []Observed
	for _, m := range regexp.MustCompile(`'(\w+)'`).FindAllSubmatch(list[1], -1) {
		states = append(states, Observed(m[1]))
	}
	return states
}

// Tasks decides which transitions an attempt's recorded state allows:
// settled is final, held is final for AppTown, refused only settles with
// nothing held. Every state the table allows is covered.
func TestEachTransitionAcceptsOnlyItsPriorStates(t *testing.T) {
	t.Parallel()
	r, in := attemptFixture(t)
	ctx := context.Background()
	states := []Observed{Reserved, Started, Collecting, Unknown, Refused, Held, Settled}
	if got := declaredStates(t); len(got) != len(states) || slices.ContainsFunc(got, func(s Observed) bool { return !slices.Contains(states, s) }) {
		t.Fatalf("the table allows %v; this test covers %v", got, states)
	}
	type transition struct {
		write    func(operation string) error
		to       Observed
		accepted []Observed
	}
	transitions := map[string]transition{
		"start":                 {func(op string) error { return r.Start(ctx, op) }, Started, []Observed{Reserved, Started, Unknown}},
		"exit":                  {func(op string) error { return r.Exited(ctx, op, 0) }, Collecting, []Observed{Reserved, Started, Unknown}},
		"timeout":               {func(op string) error { return r.TimedOut(ctx, op) }, Collecting, []Observed{Reserved, Started, Unknown}},
		"refusal":               {func(op string) error { return r.Refuse(ctx, op, "why") }, Refused, []Observed{Reserved, Started, Unknown}},
		"unknown outcome":       {func(op string) error { return r.RecordUnknown(ctx, op, "why") }, Unknown, []Observed{Reserved, Started, Collecting, Unknown}},
		"hold":                  {func(op string) error { return r.Hold(ctx, op, "why") }, Held, []Observed{Reserved, Started, Collecting, Unknown}},
		"settlement":            {func(op string) error { return r.Settle(ctx, op, nil, "why") }, Settled, []Observed{Reserved, Started, Collecting, Unknown}},
		"settled, nothing held": {func(op string) error { return r.SettleNothingHeld(ctx, op, "why") }, Settled, []Observed{Reserved, Started, Collecting, Unknown, Refused}},
	}
	n := 0
	for name, tr := range transitions {
		for _, from := range states {
			n++
			a := invocation(t, r, in, fmt.Sprint(n), "")
			reach(t, r, a.Operation, from)
			err := tr.write(a.Operation)
			got, gerr := r.Get(ctx, a.Operation)
			if gerr != nil {
				t.Fatal(gerr)
			}
			switch {
			case slices.Contains(tr.accepted, from):
				if err != nil || got.Observed != tr.to {
					t.Errorf("%s from %s: %v, now %s; want %s", name, from, err, got.Observed, tr.to)
				}
			case from == Settled && !errors.Is(err, ErrSettled), from == Held && !errors.Is(err, ErrHeld),
				from != Settled && from != Held && !errors.Is(err, ErrNotAccepted):
				t.Errorf("%s from %s = %v", name, from, err)
			case got.Observed != from:
				t.Errorf("a refused %s from %s changed it to %s", name, from, got.Observed)
			}
		}
	}
}

// A held attempt holds the only copy of its work: it is never listed for
// release, its release is never recorded, it keeps its shared workspace
// though every other task in it is released, and its run stays open.
func TestAHeldAttemptKeepsItsTaskAndWorkspace(t *testing.T) {
	t.Parallel()
	r, in := attemptFixture(t)
	ctx := context.Background()
	held := invocation(t, r, in, "agent", "step-1-a1")
	done := invocation(t, r, in, "agent-retry", "step-1-a1")
	reach(t, r, held.Operation, Held)
	reach(t, r, done.Operation, Settled)
	if err := r.ConfirmRelease(ctx, done.Operation); err != nil {
		t.Fatal(err)
	}
	if err := r.ConfirmRelease(ctx, held.Operation); err == nil {
		t.Fatal("a held attempt was recorded released")
	}
	if list, err := r.Unreleased(ctx, in.Backend, 10); err != nil || len(list) != 0 {
		t.Fatalf("listed for release: %+v %v", list, err)
	}
	if list, err := r.UnreleasedWorkspaces(ctx, in.Backend, 10); err != nil || len(list) != 0 {
		t.Fatalf("workspaces listed for release: %v %v", list, err)
	}
	if err := r.ConfirmWorkspaceRelease(ctx, in.Backend, held.Workspace); !errors.Is(err, errWorkspaceInUse) {
		t.Fatalf("releasing its workspace = %v, want %v", err, errWorkspaceInUse)
	}
	if runs, err := UnsettledRunsIn(ctx, r.db); err != nil || !slices.Equal(runs, []ids.ID{in.Owner.RunID}) {
		t.Fatalf("runs left open: %v %v", runs, err)
	}
	got, err := r.Get(ctx, held.Operation)
	if err != nil || got.Observed != Held || got.Released || got.Detail != "not kept" {
		t.Fatalf("the held attempt: %+v %v", got, err)
	}
}

// A timeout is decided in one write, with its stop, and is never erased:
// an exit is not recorded over it, even once its outcome is unknown, and
// each later transition given no exit code keeps its code and the timeout (a timeout whose stop only startup
// recovery confirms is settled with no code).
func TestARecordedTimeoutIsNeverErased(t *testing.T) {
	t.Parallel()
	r, in := attemptFixture(t)
	ctx := context.Background()
	followUps := map[string]func(string) error{
		"settlement":            func(op string) error { return r.Settle(ctx, op, nil, "stopped by recovery") },
		"settled, nothing held": func(op string) error { return r.SettleNothingHeld(ctx, op, "a different task holds its key") },
		"hold":                  func(op string) error { return r.Hold(ctx, op, "not kept") },
		"unknown outcome":       func(op string) error { return r.RecordUnknown(ctx, op, "lost") },
	}
	for name, followUp := range followUps {
		a := invocation(t, r, in, name, "")
		reach(t, r, a.Operation, Started)
		if err := r.TimedOut(ctx, a.Operation); err != nil {
			t.Fatal(err)
		}
		got, err := r.Get(ctx, a.Operation)
		if err != nil || got.Observed != Collecting || !got.TimedOut || !got.StopWanted || got.ExitCode == nil || *got.ExitCode != -1 {
			t.Fatalf("the timeout: %+v %v", got, err)
		}
		if err := r.Exited(ctx, a.Operation, 0); !errors.Is(err, ErrNotAccepted) {
			t.Fatalf("an exit over a timeout = %v, want %v", err, ErrNotAccepted)
		}
		if err := followUp(a.Operation); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := r.Exited(ctx, a.Operation, 0); err == nil {
			t.Fatalf("an exit was recorded over the timeout after its %s", name)
		}
		got, err = r.Get(ctx, a.Operation)
		if err != nil || !got.TimedOut || got.ExitCode == nil || *got.ExitCode != -1 {
			t.Fatalf("after its %s: %+v %v", name, got, err)
		}
	}
}
