package deployment_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

var errRead = errors.New("read unavailable")
var errWrite = errors.New("write unavailable")

type source struct {
	snapshot            deployment.Snapshot
	target              deployment.RecoveryTarget
	loadErr, prepareErr error
	prepared            int
}

func (s *source) LoadAttempt(context.Context, ids.ID) (deployment.Snapshot, error) {
	return s.snapshot, s.loadErr
}
func (s *source) PrepareObservation(context.Context, deployment.Attempt) (deployment.RecoveryTarget, error) {
	s.prepared++
	return s.target, s.prepareErr
}

type observer func(context.Context, deployment.Attempt, map[string]any, func(string, ...any)) (deploy.Observation, error)

func (o observer) Resume(ctx context.Context, at deployment.Attempt, checkpoint map[string]any, log func(string, ...any)) (deploy.Observation, error) {
	return o(ctx, at, checkpoint, log)
}

type settlement struct {
	calls                              []string
	attempt                            deployment.Attempt
	completion                         deployment.Completion
	cause                              error
	outcome                            deployment.Outcome
	completeErr, failErr, uncertainErr error
}

func (s *settlement) Complete(ctx context.Context, c deployment.Completion) (deployment.Outcome, error) {
	s.calls = append(s.calls, "complete")
	s.completion = c
	s.attempt = c.Attempt
	return s.outcome, s.completeErr
}
func (s *settlement) Fail(ctx context.Context, at deployment.Attempt, cause error) (deployment.Outcome, error) {
	s.calls = append(s.calls, "fail")
	s.attempt = at
	s.cause = cause
	return s.outcome, s.failErr
}
func (s *settlement) Uncertain(ctx context.Context, at deployment.Attempt, cause error) (deployment.Outcome, error) {
	s.calls = append(s.calls, "uncertain")
	s.attempt = at
	s.cause = cause
	if s.outcome == deployment.Applied {
		return deployment.Uncertain, s.uncertainErr
	}
	return s.outcome, s.uncertainErr
}

type reporter struct {
	applied  int
	releases []ids.ID
	logs     []string
}

func (r *reporter) Log(releaseID ids.ID, format string, args ...any) {
	r.releases = append(r.releases, releaseID)
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}
func (r *reporter) Applied() { r.applied++ }

func fixture(t *testing.T) (deployment.Recoverer, *source, *settlement, *reporter, *int) {
	t.Helper()
	at := deployment.Attempt{ID: idstest.N(7), DeploymentID: idstest.N(3), Generation: 2}
	s := &source{snapshot: deployment.Snapshot{Attempt: at, Status: deployment.AttemptUncertain, Checkpoint: map[string]any{"providerID": "original"}}}
	calls := new(int)
	s.target = deployment.RecoveryTarget{ReleaseID: idstest.N(11), DesiredGeneration: 2, Observer: observer(func(ctx context.Context, got deployment.Attempt, checkpoint map[string]any, log func(string, ...any)) (deploy.Observation, error) {
		*calls++
		if got != at || !reflect.DeepEqual(checkpoint, s.snapshot.Checkpoint) {
			t.Fatal("lost admitted identity")
		}
		return deploy.Observation{Generation: 2, Rollout: "active", URL: "https://app.example"}, nil
	})}
	store := &settlement{outcome: deployment.Applied}
	report := &reporter{}
	return deployment.Recoverer{Source: s, Store: store, Reporter: report}, s, store, report, calls
}

func TestRecoverySettlesOnlyConfirmedEffects(t *testing.T) {
	for _, tc := range []struct {
		name           string
		cause          error
		outcome, write string
		wantErr        bool
	}{
		{"active", nil, "recovered", "complete", false},
		{"rejected", deploy.Terminal(errors.New("provider rejected rollout")), "failed", "fail", false},
		{"lost response", errors.New("connection reset"), "uncertain", "uncertain", true},
		{"timeout", context.DeadlineExceeded, "uncertain", "uncertain", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s, store, report, calls := fixture(t)
			original := s.target.Observer
			s.target.Observer = observer(func(ctx context.Context, at deployment.Attempt, checkpoint map[string]any, log func(string, ...any)) (deploy.Observation, error) {
				o, _ := original.Resume(ctx, at, checkpoint, log)
				return o, tc.cause
			})
			got, err := r.Run(context.Background(), idstest.N(7))
			if got.Outcome != tc.outcome || (err != nil) != tc.wantErr || *calls != 1 || !reflect.DeepEqual(store.calls, []string{tc.write}) {
				t.Fatalf("result=%+v err=%v observes=%d writes=%v", got, err, *calls, store.calls)
			}
			if store.attempt != s.snapshot.Attempt {
				t.Fatal("settled different attempt")
			}
			if tc.cause == nil {
				if report.applied != 1 || store.completion.Observed.URL != "https://app.example" {
					t.Fatal("lost observed completion")
				}
			} else if report.applied != 0 {
				t.Fatal("reported success without completion")
			}
			if !reflect.DeepEqual(report.releases, []ids.ID{idstest.N(11)}) {
				t.Fatalf("log attribution: %v", report.releases)
			}
		})
	}
}

func TestRecoveryReadAndReplayDoNotContactProvider(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  deployment.AttemptState
		loadErr error
		outcome string
	}{
		{"absent", 0, deployment.ErrAttemptAbsent, "absent"},
		{"read failed", 0, errRead, ""},
		{"done", deployment.AttemptDone, nil, "done"},
		{"failed", deployment.AttemptFailed, nil, "failed"},
		{"superseded", deployment.AttemptSuperseded, nil, "superseded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s, store, report, calls := fixture(t)
			s.loadErr = tc.loadErr
			s.snapshot.Status = tc.status
			got, err := r.Run(context.Background(), idstest.N(7))
			if got.Outcome != tc.outcome || (err != nil) != (tc.loadErr == errRead) {
				t.Fatalf("%+v %v", got, err)
			}
			if s.prepared != 0 || *calls != 0 || len(store.calls) != 0 || len(report.logs) != 0 {
				t.Fatal("replay/read failure performed work")
			}
		})
	}
}

func TestRecoveryMissingInputsRemainUncertain(t *testing.T) {
	r, s, store, report, calls := fixture(t)
	s.prepareErr = errRead
	got, err := r.Run(context.Background(), idstest.N(7))
	if !errors.Is(err, errRead) || got.Outcome != "uncertain" || *calls != 0 || !reflect.DeepEqual(store.calls, []string{"uncertain"}) || report.applied != 0 {
		t.Fatalf("%+v %v calls=%v", got, err, store.calls)
	}
}

func TestRecoveryChecksGenerationBeforeObservationAndAfterSettlement(t *testing.T) {
	for _, out := range []deployment.Outcome{deployment.Superseded, deployment.AlreadyApplied} {
		for _, before := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/before=%v", out, before), func(t *testing.T) {
				r, s, store, report, calls := fixture(t)
				store.outcome = out
				if before {
					s.target.DesiredGeneration++
				}
				got, err := r.Run(context.Background(), idstest.N(7))
				wantCalls := 1
				write := "complete"
				if before {
					wantCalls = 0
					write = "fail"
				}
				if err != nil || got.Outcome != string(out) || *calls != wantCalls || !reflect.DeepEqual(store.calls, []string{write}) || report.applied != 0 || len(report.logs) != 0 {
					t.Fatalf("%+v %v observes=%d writes=%v report=%+v", got, err, *calls, store.calls, report)
				}
			})
		}
	}
}

func TestRecoveryCancellationDoesNotSettle(t *testing.T) {
	for _, after := range []bool{false, true} {
		for _, cause := range []error{nil, errors.New("lost response"), deploy.Terminal(errors.New("rejected"))} {
			t.Run(fmt.Sprintf("after=%v/cause=%v", after, cause), func(t *testing.T) {
				r, s, store, report, calls := fixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				original := s.target.Observer
				s.target.Observer = observer(func(ctx context.Context, at deployment.Attempt, checkpoint map[string]any, log func(string, ...any)) (deploy.Observation, error) {
					o, _ := original.Resume(ctx, at, checkpoint, log)
					cancel()
					return o, cause
				})
				if !after {
					cancel()
				}
				_, err := r.Run(ctx, idstest.N(7))
				wantCalls := 0
				if after {
					wantCalls = 1
				}
				if !errors.Is(err, context.Canceled) || *calls != wantCalls || len(store.calls) != 0 || len(report.logs) != 0 || report.applied != 0 {
					t.Fatalf("err=%v calls=%v report=%+v", err, store.calls, report)
				}
			})
		}
	}
}

func TestRecoverySettlementWriteFailureCannotReportSuccess(t *testing.T) {
	for _, uncertainFails := range []bool{false, true} {
		t.Run(fmt.Sprint(uncertainFails), func(t *testing.T) {
			r, _, store, report, _ := fixture(t)
			store.completeErr = errWrite
			if uncertainFails {
				store.uncertainErr = errWrite
			}
			got, err := r.Run(context.Background(), idstest.N(7))
			if !errors.Is(err, errWrite) || report.applied != 0 || !reflect.DeepEqual(store.calls, []string{"complete", "uncertain"}) {
				t.Fatalf("%+v %v writes=%v", got, err, store.calls)
			}
			if uncertainFails && (got.Outcome != "" || len(report.logs) != 0) {
				t.Fatal("reported unwritten uncertainty")
			}
			if !uncertainFails && got.Outcome != "uncertain" {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestRecoveryFailureWriteAndConcurrentUncertaintySettlement(t *testing.T) {
	r, s, store, report, _ := fixture(t)
	s.target.Observer = observer(func(context.Context, deployment.Attempt, map[string]any, func(string, ...any)) (deploy.Observation, error) {
		return deploy.Observation{}, deploy.Terminal(errors.New("rejected"))
	})
	store.failErr = errWrite
	if _, err := r.Run(context.Background(), idstest.N(7)); !errors.Is(err, errWrite) || len(report.logs) != 0 {
		t.Fatalf("failed settlement: %v logs=%v", err, report.logs)
	}
	for _, out := range []deployment.Outcome{deployment.Superseded, deployment.AlreadyApplied} {
		r, s, store, report, _ := fixture(t)
		s.prepareErr = errRead
		store.outcome = out
		got, err := r.Run(context.Background(), idstest.N(7))
		if err != nil || got.Outcome != string(out) || len(report.logs) != 0 || report.applied != 0 {
			t.Fatalf("concurrent settlement: %+v %v", got, err)
		}
	}
}
