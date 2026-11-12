package workflow

import (
	"sync/atomic"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/ids"
)

func TestRepairStartsOnlyAfterFailureAndNeverExceedsTheRunsAllowance(t *testing.T) {
	var starts atomic.Int32
	rig := stepAuthorityRunnerFixture(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{
		{Name: "repair", Uses: "agent/pi", With: map[string]any{"repairRounds": 2}},
		{Name: "tests", Uses: "tests"},
	}}}}, map[string]Runner{
		"agent/pi": func(task *Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			starts.Add(1)
			if err := task.Finish(EncodeOutcome(StatusPass, map[string]any{"reason": "repair written"}, nil)); err != nil {
				t.Error(err)
			}
		},
		"tests": func(task *Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			if err := task.Finish(EncodeOutcome(StatusFail, map[string]any{"reason": "the regression persists"}, nil)); err != nil {
				t.Error(err)
			}
		},
	})
	rig.Work.Stop()
	ctx := stepVerifiedContext(t.Context(), access.Principal{Kind: "user", Name: "owner", Via: "token"})
	var repair, tests ids.ID
	snapshot, err := rig.Work.Snapshot(ctx, rig.RunID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snapshot.Steps {
		if s.Name == "repair" {
			repair = s.ID
			if s.Status != StatusNotApplicable {
				t.Fatalf("repair ran on entry: %+v", s)
			}
		} else if s.Name == "tests" {
			tests = s.ID
		}
	}
	if starts.Load() != 0 {
		t.Fatal("repair started without a failure")
	}
	for round := 0; round < 3; round++ {
		if err := rig.Work.ExecuteStep(ctx, tests); err != nil {
			t.Fatal(err)
		}
		snapshot, err = rig.Work.Snapshot(ctx, rig.RunID, DetailView)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range snapshot.Steps {
			if s.ID == repair {
				want := StatusPending
				if round == 2 {
					want = StatusPass
				}
				if s.Status != want {
					t.Fatalf("after failure %d repair=%s want %s", round+1, s.Status, want)
				}
			}
		}
		if round < 2 {
			if err := rig.Work.ExecuteStep(ctx, repair); err != nil {
				t.Fatal(err)
			}
		}
	}
	if starts.Load() != 2 {
		t.Fatalf("started %d repair sessions", starts.Load())
	}
}
