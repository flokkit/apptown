package deployment_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/ids/idstest"
)

type executionStore struct {
	settlement
	start                   deployment.Start
	admitted                deployment.Attempt
	created                 bool
	admitErr, checkpointErr error
	onAdmit                 func()
	checkpoint              map[string]any
}

func (s *executionStore) Admit(ctx context.Context, in deployment.Start) (deployment.Attempt, bool, error) {
	s.calls = append(s.calls, "admit")
	s.start = in
	if s.onAdmit != nil {
		s.onAdmit()
	}
	return s.admitted, s.created, s.admitErr
}
func (s *executionStore) Checkpoint(ctx context.Context, at deployment.Attempt) deploy.Checkpoint {
	return func(values map[string]any) error {
		s.calls = append(s.calls, "checkpoint")
		if at != s.admitted {
			return errors.New("checkpoint bound to wrong attempt")
		}
		s.checkpoint = values
		return s.checkpointErr
	}
}
func executionFixture() (deployment.Runner, *executionStore, deployment.Start, *int) {
	s := &executionStore{settlement: settlement{outcome: deployment.Applied}, admitted: deployment.Attempt{ID: idstest.N(7), DeploymentID: idstest.N(3), Generation: 2}, created: true}
	count := new(int)
	base := int64(1)
	in := deployment.Start{DeploymentID: idstest.N(3), TargetID: idstest.N(5), Key: "release-11", BaseGeneration: &base,
		Inputs: deployment.Inputs{ReleaseID: idstest.N(11), Revision: "selected-revision", ImageDigest: "selected-image", ArtifactHash: "selected-artifact"}}
	return deployment.Runner{Store: s, Applied: func() { *count++ }}, s, in, count
}
func activeExecution(_ context.Context, at deployment.Attempt, checkpoint deploy.Checkpoint) (deploy.Observation, error) {
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
			got, err := r.Run(context.Background(), in, func(context.Context, deployment.Attempt, deploy.Checkpoint) (deploy.Observation, error) {
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
				var failure *deployment.RunError
				if !errors.As(err, &failure) || failure.Stage != deployment.Admission || !errors.Is(err, errWrite) {
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
		out   deployment.Outcome
	}{
		{"active", nil, "complete", deployment.Applied},
		{"rejected", fmt.Errorf("provider: %w", deploy.Terminal(errors.New("rejected"))), "fail", deployment.Applied},
		{"lost response", errors.New("lost response"), "uncertain", deployment.Uncertain},
		{"timeout", context.DeadlineExceeded, "uncertain", deployment.Uncertain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s, in, applied := executionFixture()
			got, err := r.Run(context.Background(), in, func(ctx context.Context, at deployment.Attempt, cp deploy.Checkpoint) (deploy.Observation, error) {
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
	got, err := r.Run(context.Background(), in, func(ctx context.Context, at deployment.Attempt, cp deploy.Checkpoint) (deploy.Observation, error) {
		if _, err := activeExecution(ctx, at, cp); err != nil {
			return deploy.Observation{}, err
		}
		effect = true
		return deploy.Observation{}, nil
	})
	if err == nil || !deploy.IsTerminal(err) || effect || *applied != 0 || got.Outcome != deployment.Applied || !reflect.DeepEqual(s.calls, []string{"admit", "checkpoint", "fail"}) {
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
				got, err := r.Run(ctx, in, func(ctx context.Context, at deployment.Attempt, cp deploy.Checkpoint) (deploy.Observation, error) {
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
				if when == "after execution" && got.Outcome != deployment.Uncertain {
					t.Fatal("published cancelled execution")
				}
			})
		}
	}
}
func TestExecutionLateSettlementCannotNotifySuccess(t *testing.T) {
	for _, out := range []deployment.Outcome{deployment.Superseded, deployment.AlreadyApplied} {
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
				wantStage := deployment.Settlement
				write := "complete"
				if terminal {
					cause = deploy.Terminal(errors.New("rejected"))
					s.failErr = errWrite
					wantStage = deployment.Execution
					write = "fail"
				} else {
					s.completeErr = errWrite
				}
				queueErr := errors.New("queue unavailable")
				if queueFails {
					s.uncertainErr = queueErr
				}
				got, err := r.Run(context.Background(), in, func(ctx context.Context, at deployment.Attempt, cp deploy.Checkpoint) (deploy.Observation, error) {
					o, _ := activeExecution(ctx, at, cp)
					return o, cause
				})
				var failure *deployment.RunError
				if !errors.As(err, &failure) || failure.Stage != wantStage || deploy.IsTerminal(err) || *applied != 0 || !reflect.DeepEqual(s.calls, []string{"admit", "checkpoint", write, "uncertain"}) {
					t.Fatalf("%+v %v calls=%v", got, err, s.calls)
				}
				if queueFails {
					if !errors.Is(err, queueErr) {
						t.Fatalf("lost queue failure: %v", err)
					}
				} else if !errors.Is(err, errWrite) || got.Outcome != deployment.Uncertain {
					t.Fatalf("lost settlement failure: %+v %v", got, err)
				}
			})
		}
	}
}
func TestExecutionLostUncertaintyWriteIsNotReportedAsSuccess(t *testing.T) {
	r, s, in, applied := executionFixture()
	s.uncertainErr = errWrite
	_, err := r.Run(context.Background(), in, func(context.Context, deployment.Attempt, deploy.Checkpoint) (deploy.Observation, error) {
		return deploy.Observation{}, errors.New("lost provider response")
	})
	if !errors.Is(err, errWrite) || deploy.IsTerminal(err) || *applied != 0 || !reflect.DeepEqual(s.calls, []string{"admit", "uncertain"}) {
		t.Fatalf("%v calls=%v", err, s.calls)
	}
}
