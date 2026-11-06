package deployment

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type startupFixtureStore struct {
	attempts               []Attempt
	discoverErr, verifyErr error
	failAt                 ids.ID
	contradictAt           ids.ID // an attempt whose records contradict each other
	stranded               []Contradiction
	calls                  []string
	queued                 []ids.ID
	flagged                []ids.ID
}

func (s *startupFixtureStore) InterruptedAttempts(ctx context.Context) ([]Attempt, error) {
	s.calls = append(s.calls, "discover")
	return s.attempts, s.discoverErr
}
func (s *startupFixtureStore) ContradictorySettlements(ctx context.Context) ([]Contradiction, error) {
	s.calls = append(s.calls, "verify")
	return s.stranded, s.verifyErr
}
func (s *startupFixtureStore) NeedsAttention(ctx context.Context, at Attempt, _ error) error {
	s.calls = append(s.calls, "flag")
	s.flagged = append(s.flagged, at.ID)
	return nil
}
func (s *startupFixtureStore) Uncertain(ctx context.Context, at Attempt, _ error) (Outcome, error) {
	s.calls = append(s.calls, "queue")
	if at.ID == s.failAt {
		return "", errWrite
	}
	if at.ID == s.contradictAt {
		return "", ErrReadinessTransition
	}
	if at.ID == idstest.N(2) {
		return Superseded, nil
	}
	s.queued = append(s.queued, at.ID)
	return Uncertain, nil
}
func TestStartupQueuesOnlyAfterCompleteDiscoveryAndVerification(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		discoverErr, verifyErr error
		failAt                 ids.ID
		want                   int
		calls                  []string
	}{
		{name: "discovery failed", discoverErr: errRead, calls: []string{"discover"}},
		{name: "verification failed", verifyErr: errRead, calls: []string{"discover", "verify"}},
		{name: "queue failed", failAt: idstest.N(3), want: 1, calls: []string{"discover", "verify", "queue", "queue", "queue"}},
		{name: "queued", want: 2, calls: []string{"discover", "verify", "queue", "queue", "queue"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &startupFixtureStore{attempts: []Attempt{{ID: idstest.N(1)}, {ID: idstest.N(2)}, {ID: idstest.N(3)}}, discoverErr: tc.discoverErr, verifyErr: tc.verifyErr, failAt: tc.failAt}
			got, err := PrepareRecovery(context.Background(), s)
			wantErr := tc.discoverErr
			if tc.verifyErr != nil {
				wantErr = tc.verifyErr
			}
			if !tc.failAt.IsZero() {
				wantErr = errWrite
			}
			if !errors.Is(err, wantErr) || got.Queued != tc.want || len(s.queued) != tc.want || !reflect.DeepEqual(s.calls, tc.calls) {
				t.Fatalf("report=%+v err=%v queued=%v calls=%v", got, err, s.queued, s.calls)
			}
		})
	}
}
func TestStartupWithNoInterruptedAttemptsStillVerifiesSettlement(t *testing.T) {
	s := &startupFixtureStore{}
	got, err := PrepareRecovery(context.Background(), s)
	if err != nil || got.Queued != 0 || !reflect.DeepEqual(s.calls, []string{"discover", "verify"}) {
		t.Fatalf("%+v %v calls=%v", got, err, s.calls)
	}
}

// A deployment whose records contradict each other does not stop startup:
// it is flagged, and the others are queued as usual.
func TestStartupFlagsContradictoryDeploymentsAndGoesOn(t *testing.T) {
	s := &startupFixtureStore{attempts: []Attempt{{ID: idstest.N(1)}, {ID: idstest.N(3)}, {ID: idstest.N(4)}}, contradictAt: idstest.N(3),
		stranded: []Contradiction{{Attempt: Attempt{ID: idstest.N(9)}, Status: AttemptDone, ReleaseID: idstest.N(5)}}}
	got, err := PrepareRecovery(context.Background(), s)
	if err != nil || got.Queued != 2 || got.NeedsAttention != 2 || !reflect.DeepEqual(s.flagged, []ids.ID{idstest.N(9), idstest.N(3)}) || !reflect.DeepEqual(s.queued, []ids.ID{idstest.N(1), idstest.N(4)}) {
		t.Fatalf("report=%+v err=%v flagged=%v queued=%v", got, err, s.flagged, s.queued)
	}
}
