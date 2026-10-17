package deployment_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

type startupStore struct {
	attempts               []deployment.Attempt
	discoverErr, verifyErr error
	failAt                 ids.ID
	contradictAt           ids.ID // an attempt whose records contradict each other
	stranded               []deployment.Contradiction
	calls                  []string
	queued                 []ids.ID
	flagged                []ids.ID
}

func (s *startupStore) InterruptedAttempts(ctx context.Context) ([]deployment.Attempt, error) {
	s.calls = append(s.calls, "discover")
	return s.attempts, s.discoverErr
}
func (s *startupStore) ContradictorySettlements(ctx context.Context) ([]deployment.Contradiction, error) {
	s.calls = append(s.calls, "verify")
	return s.stranded, s.verifyErr
}
func (s *startupStore) NeedsAttention(ctx context.Context, at deployment.Attempt, _ error) error {
	s.calls = append(s.calls, "flag")
	s.flagged = append(s.flagged, at.ID)
	return nil
}
func (s *startupStore) Uncertain(ctx context.Context, at deployment.Attempt, _ error) (deployment.Outcome, error) {
	s.calls = append(s.calls, "queue")
	if at.ID == s.failAt {
		return "", errWrite
	}
	if at.ID == s.contradictAt {
		return "", deployment.ErrReadinessTransition
	}
	if at.ID == idstest.N(2) {
		return deployment.Superseded, nil
	}
	s.queued = append(s.queued, at.ID)
	return deployment.Uncertain, nil
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
			s := &startupStore{attempts: []deployment.Attempt{{ID: idstest.N(1)}, {ID: idstest.N(2)}, {ID: idstest.N(3)}}, discoverErr: tc.discoverErr, verifyErr: tc.verifyErr, failAt: tc.failAt}
			got, err := deployment.PrepareRecovery(context.Background(), s)
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
	s := &startupStore{}
	got, err := deployment.PrepareRecovery(context.Background(), s)
	if err != nil || got.Queued != 0 || !reflect.DeepEqual(s.calls, []string{"discover", "verify"}) {
		t.Fatalf("%+v %v calls=%v", got, err, s.calls)
	}
}

// A deployment whose records contradict each other does not stop startup:
// it is flagged, and the others are queued as usual.
func TestStartupFlagsContradictoryDeploymentsAndGoesOn(t *testing.T) {
	s := &startupStore{attempts: []deployment.Attempt{{ID: idstest.N(1)}, {ID: idstest.N(3)}, {ID: idstest.N(4)}}, contradictAt: idstest.N(3),
		stranded: []deployment.Contradiction{{Attempt: deployment.Attempt{ID: idstest.N(9)}, Status: deployment.AttemptDone, ReleaseID: idstest.N(5)}}}
	got, err := deployment.PrepareRecovery(context.Background(), s)
	if err != nil || got.Queued != 2 || got.NeedsAttention != 2 || !reflect.DeepEqual(s.flagged, []ids.ID{idstest.N(9), idstest.N(3)}) || !reflect.DeepEqual(s.queued, []ids.ID{idstest.N(1), idstest.N(4)}) {
		t.Fatalf("report=%+v err=%v flagged=%v queued=%v", got, err, s.flagged, s.queued)
	}
}
