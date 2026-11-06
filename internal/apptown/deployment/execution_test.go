package deployment

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type executionFixtureStore struct {
	executionSettlement
	start                   Start
	admitted                Attempt
	created                 bool
	admitErr, checkpointErr error
	onAdmit                 func()
	checkpoint              map[string]any
}

func (s *executionFixtureStore) Admit(ctx context.Context, in Start) (Attempt, bool, error) {
	s.calls = append(s.calls, "admit")
	s.start = in
	if s.onAdmit != nil {
		s.onAdmit()
	}
	return s.admitted, s.created, s.admitErr
}
func (s *executionFixtureStore) Checkpoint(ctx context.Context, at Attempt) deploy.Checkpoint {
	return func(values map[string]any) error {
		s.calls = append(s.calls, "checkpoint")
		if at != s.admitted {
			return errors.New("checkpoint bound to wrong attempt")
		}
		s.checkpoint = values
		return s.checkpointErr
	}
}
func executionFixture() (Runner, *executionFixtureStore, Start, *int) {
	s := &executionFixtureStore{executionSettlement: executionSettlement{outcome: Applied}, admitted: Attempt{ID: idstest.N(7), DeploymentID: idstest.N(3), Generation: 2}, created: true}
	count := new(int)
	base := int64(1)
	in := Start{DeploymentID: idstest.N(3), TargetID: idstest.N(5), Key: "release-11", BaseGeneration: &base,
		Inputs: Inputs{ReleaseID: idstest.N(11), Revision: "selected-revision", ImageDigest: "selected-image", ArtifactHash: "selected-artifact"}}
	return Runner{Store: s, Applied: func(_ ids.ID) { *count++ }}, s, in, count
}
func activeExecution(_ context.Context, at Attempt, checkpoint deploy.Checkpoint) (deploy.Observation, error) {
	if err := checkpoint(map[string]any{"providerID": "original"}); err != nil {
		return deploy.Observation{}, deploy.Terminal(err)
	}
	return deploy.Observation{Generation: at.Generation, Rollout: "active", URL: "https://original.example"}, nil
}

func TestExecutionAdmissionAndReplayNeverInvokeProvider(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprint(replay), func(t *testing.T) {
			r, s, in, applied := executionFixture()
			s.created = false
			if !replay {
				s.admitErr = errWrite
			}
			got, err := r.Run(context.Background(), in, func(context.Context, Attempt, deploy.Checkpoint) (deploy.Observation, error) {
				t.Fatal("unadmitted execution")
				return deploy.Observation{}, nil
			})
			if (err != nil) != (!replay) || got.Created || *applied != 0 || !reflect.DeepEqual(s.calls, []string{"admit"}) {
				t.Fatalf("%+v %v calls=%v", got, err, s.calls)
			}
			if !reflect.DeepEqual(s.start, in) {
				t.Fatal("changed admission request")
			}
			if !replay {
				var failure *runError
				if !errors.As(err, &failure) || failure.Stage != admission || !errors.Is(err, errWrite) {
					t.Fatalf("admission cause: %v", err)
				}
			}
		})
	}
}
func TestExecutionSettlesProviderResultAndNotifiesOnlyCommittedSuccess(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		write string
		out   Outcome
	}{
		{"active", nil, "complete", Applied},
		{"rejected", fmt.Errorf("provider: %w", deploy.Terminal(errors.New("rejected"))), "fail", Applied},
		{"lost response", errors.New("lost response"), "uncertain", Uncertain},
		{"timeout", context.DeadlineExceeded, "uncertain", Uncertain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s, in, applied := executionFixture()
			got, err := r.Run(context.Background(), in, func(ctx context.Context, at Attempt, cp deploy.Checkpoint) (deploy.Observation, error) {
				if _, set := ctx.Deadline(); set {
					t.Fatal("runner imposed a deadline")
				}
				if at != s.admitted {
					t.Fatal("wrong execution identity")
				}
				o, err := activeExecution(ctx, at, cp)
				if err != nil {
					t.Fatal(err)
				}
				return o, tc.cause
			})
			if (err != nil) != (tc.cause != nil) || got.Outcome != tc.out || !got.Created || !reflect.DeepEqual(s.calls, []string{"admit", "checkpoint", tc.write}) {
				t.Fatalf("%+v %v calls=%v", got, err, s.calls)
			}
			wantApplied := 0
			if tc.cause == nil {
				wantApplied = 1
				if !reflect.DeepEqual(s.completion.Observed, got.Observed) {
					t.Fatal("lost observation")
				}
			}
			if *applied != wantApplied || s.attempt != s.admitted {
				t.Fatalf("applied=%d identity=%+v", *applied, s.attempt)
			}
			if tc.cause != nil && deploy.IsTerminal(err) != deploy.IsTerminal(tc.cause) {
				t.Fatalf("changed retry classification: %v", err)
			}
		})
	}
}
func TestExecutionCheckpointFailureStopsProviderEffect(t *testing.T) {
	r, s, in, applied := executionFixture()
	s.checkpointErr = errWrite
	effect := false
	got, err := r.Run(context.Background(), in, func(ctx context.Context, at Attempt, cp deploy.Checkpoint) (deploy.Observation, error) {
		if _, err := activeExecution(ctx, at, cp); err != nil {
			return deploy.Observation{}, err
		}
		effect = true
		return deploy.Observation{}, nil
	})
	if err == nil || !deploy.IsTerminal(err) || effect || *applied != 0 || got.Outcome != Applied || !reflect.DeepEqual(s.calls, []string{"admit", "checkpoint", "fail"}) {
		t.Fatalf("%+v %v calls=%v", got, err, s.calls)
	}
}
func TestExecutionCancellationDistinguishesUnstartedAndUnknownEffects(t *testing.T) {
	for _, when := range []string{"before admission", "after admission", "after execution"} {
		for _, cause := range []error{nil, errors.New("lost response"), deploy.Terminal(errors.New("rejected"))} {
			t.Run(fmt.Sprint(when, "/", cause), func(t *testing.T) {
				r, s, in, applied := executionFixture()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if when == "before admission" {
					cancel()
				}
				if when == "after admission" {
					s.onAdmit = cancel
				}
				calls := 0
				got, err := r.Run(ctx, in, func(ctx context.Context, at Attempt, cp deploy.Checkpoint) (deploy.Observation, error) {
					calls++
					o, _ := activeExecution(ctx, at, cp)
					cancel()
					return o, cause
				})
				var want []string
				wantExecution := 0
				switch when {
				case "after admission":
					want = []string{"admit", "fail"}
				case "after execution":
					want = []string{"admit", "checkpoint", "uncertain"}
					wantExecution = 1
				}
				if !errors.Is(err, context.Canceled) || calls != wantExecution || *applied != 0 || !reflect.DeepEqual(s.calls, want) {
					t.Fatalf("%+v %v effects=%d writes=%v", got, err, calls, s.calls)
				}
				if when == "after execution" && got.Outcome != Uncertain {
					t.Fatal("published cancelled execution")
				}
			})
		}
	}
}
func TestExecutionLateSettlementCannotNotifySuccess(t *testing.T) {
	for _, out := range []Outcome{Superseded, AlreadyApplied} {
		r, s, in, applied := executionFixture()
		s.outcome = out
		got, err := r.Run(context.Background(), in, activeExecution)
		if err != nil || got.Outcome != out || *applied != 0 {
			t.Fatalf("%+v %v notifications=%d", got, err, *applied)
		}
	}
}
func TestExecutionLostReceiptQueuesRecoveryAndRemainsRetryable(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		for _, queueFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("terminal=%v/queueFails=%v", terminal, queueFails), func(t *testing.T) {
				r, s, in, applied := executionFixture()
				var cause error
				wantStage := settlement
				write := "complete"
				if terminal {
					cause = deploy.Terminal(errors.New("rejected"))
					s.failErr = errWrite
					wantStage = Execution
					write = "fail"
				} else {
					s.completeErr = errWrite
				}
				queueErr := errors.New("queue unavailable")
				if queueFails {
					s.uncertainErr = queueErr
				}
				got, err := r.Run(context.Background(), in, func(ctx context.Context, at Attempt, cp deploy.Checkpoint) (deploy.Observation, error) {
					o, _ := activeExecution(ctx, at, cp)
					return o, cause
				})
				var failure *runError
				if !errors.As(err, &failure) || failure.Stage != wantStage || deploy.IsTerminal(err) || *applied != 0 || !reflect.DeepEqual(s.calls, []string{"admit", "checkpoint", write, "uncertain"}) {
					t.Fatalf("%+v %v calls=%v", got, err, s.calls)
				}
				if queueFails {
					if !errors.Is(err, queueErr) {
						t.Fatalf("lost queue failure: %v", err)
					}
				} else if !errors.Is(err, errWrite) || got.Outcome != Uncertain {
					t.Fatalf("lost executionSettlement failure: %+v %v", got, err)
				}
			})
		}
	}
}
func TestExecutionLostUncertaintyWriteIsNotReportedAsSuccess(t *testing.T) {
	r, s, in, applied := executionFixture()
	s.uncertainErr = errWrite
	_, err := r.Run(context.Background(), in, func(context.Context, Attempt, deploy.Checkpoint) (deploy.Observation, error) {
		return deploy.Observation{}, errors.New("lost provider response")
	})
	if !errors.Is(err, errWrite) || deploy.IsTerminal(err) || *applied != 0 || !reflect.DeepEqual(s.calls, []string{"admit", "uncertain"}) {
		t.Fatalf("%v calls=%v", err, s.calls)
	}
}

func TestExecutionRecordsActBeforeEvenAFailedEffect(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(fmt.Sprint(refuse), func(t *testing.T) {
			r, _, in, _ := executionFixture()
			marked, effect := false, false
			r.BeforeAct = func(_ context.Context, id ids.ID) error {
				if id != in.TargetID {
					t.Errorf("marked target %s, want %s", id, in.TargetID)
				}
				marked = true
				if refuse {
					return errors.New("counter write failed")
				}
				return nil
			}
			_, err := r.Run(t.Context(), in, func(context.Context, Attempt, deploy.Checkpoint) (deploy.Observation, error) {
				if !marked {
					t.Error("effect preceded act counter")
				}
				effect = true
				return deploy.Observation{}, deploy.Terminal(errors.New("effect failed"))
			})
			if err == nil || !marked || effect == refuse {
				t.Fatalf("marked=%v effect=%v refuse=%v err=%v", marked, effect, refuse, err)
			}
		})
	}
}
