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

var errRead = errors.New("read unavailable")
var errWrite = errors.New("write unavailable")

type recoveryFixtureSource struct {
	snapshot            Snapshot
	target              RecoveryTarget
	loadErr, prepareErr error
	prepared            int
}

func (s *recoveryFixtureSource) LoadAttempt(context.Context, ids.ID) (Snapshot, error) {
	return s.snapshot, s.loadErr
}
func (s *recoveryFixtureSource) PrepareObservation(context.Context, Attempt) (RecoveryTarget, error) {
	s.prepared++
	return s.target, s.prepareErr
}

type recoveryFixtureObserver func(context.Context, Attempt, map[string]any, func(string, ...any)) (deploy.Observation, error)

func (o recoveryFixtureObserver) Resume(ctx context.Context, at Attempt, checkpoint map[string]any, log func(string, ...any)) (deploy.Observation, error) {
	return o(ctx, at, checkpoint, log)
}

type executionSettlement struct {
	calls                              []string
	attempt                            Attempt
	completion                         Completion
	cause                              error
	outcome                            Outcome
	completeErr, failErr, uncertainErr error
}

func (s *executionSettlement) Complete(ctx context.Context, c Completion) (Outcome, error) {
	s.calls = append(s.calls, "complete")
	s.completion = c
	s.attempt = c.Attempt
	return s.outcome, s.completeErr
}
func (s *executionSettlement) Fail(ctx context.Context, at Attempt, cause error) (Outcome, error) {
	s.calls = append(s.calls, "fail")
	s.attempt = at
	s.cause = cause
	return s.outcome, s.failErr
}
func (s *executionSettlement) Uncertain(ctx context.Context, at Attempt, cause error) (Outcome, error) {
	s.calls = append(s.calls, "uncertain")
	s.attempt = at
	s.cause = cause
	if s.outcome == Applied {
		return Uncertain, s.uncertainErr
	}
	return s.outcome, s.uncertainErr
}

type recoveryFixtureReporter struct {
	applied  int
	releases []ids.ID
	logs     []string
}

func (r *recoveryFixtureReporter) Log(releaseID ids.ID, format string, args ...any) {
	r.releases = append(r.releases, releaseID)
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}
func (r *recoveryFixtureReporter) Applied(ids.ID) { r.applied++ }

func recoveryFixture(t *testing.T) (Recoverer, *recoveryFixtureSource, *executionSettlement, *recoveryFixtureReporter, *int) {
	t.Helper()
	at := Attempt{ID: idstest.N(7), DeploymentID: idstest.N(3), Generation: 2}
	s := &recoveryFixtureSource{snapshot: Snapshot{Attempt: at, Status: AttemptUncertain, Checkpoint: map[string]any{"providerID": "original"}}}
	calls := new(int)
	s.target = RecoveryTarget{ReleaseID: idstest.N(11), Observer: recoveryFixtureObserver(func(ctx context.Context, got Attempt, checkpoint map[string]any, log func(string, ...any)) (deploy.Observation, error) {
		*calls++
		if got != at || !reflect.DeepEqual(checkpoint, s.snapshot.Checkpoint) {
			t.Fatal("lost admitted identity")
		}
		return deploy.Observation{Generation: 2, Rollout: "active", URL: "https://app.example"}, nil
	})}
	store := &executionSettlement{outcome: Applied}
	report := &recoveryFixtureReporter{}
	return Recoverer{source: s, store: store, reporter: report}, s, store, report, calls
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
			r, s, store, report, calls := recoveryFixture(t)
			original := s.target.Observer
			s.target.Observer = recoveryFixtureObserver(func(ctx context.Context, at Attempt, checkpoint map[string]any, log func(string, ...any)) (deploy.Observation, error) {
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
		status  AttemptState
		loadErr error
		outcome string
	}{
		{"absent", 0, ErrAttemptAbsent, "absent"},
		{"read failed", 0, errRead, ""},
		{"done", AttemptDone, nil, "done"},
		{"failed", AttemptFailed, nil, "failed"},
		{"superseded", AttemptSuperseded, nil, "superseded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s, store, report, calls := recoveryFixture(t)
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
	r, s, store, report, calls := recoveryFixture(t)
	s.prepareErr = errRead
	got, err := r.Run(context.Background(), idstest.N(7))
	if !errors.Is(err, errRead) || got.Outcome != "uncertain" || *calls != 0 || !reflect.DeepEqual(store.calls, []string{"uncertain"}) || report.applied != 0 {
		t.Fatalf("%+v %v calls=%v", got, err, store.calls)
	}
}

// Ownership is the attempt's status, decided where it settles: recovery of an
// open attempt observes it whatever the deployment's desired generation.
func TestRecoveryChecksOwnershipAtSettlement(t *testing.T) {
	for _, out := range []Outcome{Superseded, AlreadyApplied} {
		t.Run(string(out), func(t *testing.T) {
			r, _, store, report, calls := recoveryFixture(t)
			store.outcome = out
			got, err := r.Run(context.Background(), idstest.N(7))
			if err != nil || got.Outcome != string(out) || *calls != 1 || !reflect.DeepEqual(store.calls, []string{"complete"}) || report.applied != 0 || len(report.logs) != 0 {
				t.Fatalf("%+v %v observes=%d writes=%v report=%+v", got, err, *calls, store.calls, report)
			}
		})
	}
}

func TestRecoveryCancellationDoesNotSettle(t *testing.T) {
	for _, after := range []bool{false, true} {
		for _, cause := range []error{nil, errors.New("lost response"), deploy.Terminal(errors.New("rejected"))} {
			t.Run(fmt.Sprintf("after=%v/cause=%v", after, cause), func(t *testing.T) {
				r, s, store, report, calls := recoveryFixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				original := s.target.Observer
				s.target.Observer = recoveryFixtureObserver(func(ctx context.Context, at Attempt, checkpoint map[string]any, log func(string, ...any)) (deploy.Observation, error) {
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
			r, _, store, report, _ := recoveryFixture(t)
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
	r, s, store, report, _ := recoveryFixture(t)
	s.target.Observer = recoveryFixtureObserver(func(context.Context, Attempt, map[string]any, func(string, ...any)) (deploy.Observation, error) {
		return deploy.Observation{}, deploy.Terminal(errors.New("rejected"))
	})
	store.failErr = errWrite
	if _, err := r.Run(context.Background(), idstest.N(7)); !errors.Is(err, errWrite) || len(report.logs) != 0 {
		t.Fatalf("failed executionSettlement: %v logs=%v", err, report.logs)
	}
	for _, out := range []Outcome{Superseded, AlreadyApplied} {
		r, s, store, report, _ := recoveryFixture(t)
		s.prepareErr = errRead
		store.outcome = out
		got, err := r.Run(context.Background(), idstest.N(7))
		if err != nil || got.Outcome != string(out) || len(report.logs) != 0 || report.applied != 0 {
			t.Fatalf("concurrent executionSettlement: %+v %v", got, err)
		}
	}
}

func TestRecoveryRetriesOnlyAnUnappliedVersionAndPreservesFailureClassification(t *testing.T) {
	for _, scenario := range []string{"already up", "provider failed", "unreachable", "retry succeeds", "retry fails", "retry unknown", "unapplied before checkpoint", "unapplied, no restart"} {
		t.Run(scenario, func(t *testing.T) {
			r, source, store, _, _ := recoveryFixture(t)
			if scenario != "unapplied, no restart" {
				source.snapshot.Checkpoint["_restart"] = true
			}
			observation := deploy.Observation{Generation: 2, Rollout: "active"}
			source.target.Observer = recoveryFixtureObserver(func(context.Context, Attempt, map[string]any, func(string, ...any)) (deploy.Observation, error) {
				switch scenario {
				case "already up":
					return observation, nil
				case "provider failed":
					return deploy.Observation{}, deploy.Terminal(errors.New("provider rejected"))
				case "unreachable":
					return deploy.Observation{}, errors.New("host unavailable")
				case "unapplied before checkpoint", "unapplied, no restart":
					// A driver interrupted before its first checkpoint applied nothing.
					return deploy.Observation{}, deploy.Terminal(fmt.Errorf("no app was named: %w", deploy.ErrNotApplied))
				default:
					return deploy.Observation{}, deploy.ErrNotApplied
				}
			})
			retries := 0
			r.retry = func(_ context.Context, at Attempt) (deploy.Observation, error) {
				retries++
				if at != source.snapshot.Attempt {
					t.Fatal("retry changed attempt or generation")
				}
				switch scenario {
				case "retry fails":
					return deploy.Observation{}, deploy.Terminal(errors.New("provider rejected"))
				case "retry unknown":
					return deploy.Observation{}, errors.New("host unavailable")
				default:
					return observation, nil
				}
			}
			result, err := r.Run(t.Context(), source.snapshot.Attempt.ID)
			want, calls := "recovered", 0
			switch scenario {
			case "provider failed":
				want = "failed"
			case "unreachable":
				want = "uncertain"
			case "retry succeeds", "unapplied before checkpoint":
				calls = 1
			case "unapplied, no restart":
				want = "failed"
			case "retry fails":
				want, calls = "failed", 1
			case "retry unknown":
				want, calls = "uncertain", 1
			}
			if result.Outcome != want || retries != calls || (err != nil) != (want == "uncertain") {
				t.Fatalf("%s: %+v %v retries=%d store=%v", scenario, result, err, retries, store.calls)
			}
		})
	}
}
