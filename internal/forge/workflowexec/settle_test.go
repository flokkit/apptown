package workflowexec

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids/idstest"
)

// settleProduced retries only the recording, stops at the attempt fence, and
// ends in a stated outcome for every way the recording can go.
func TestSettleProducedEndsInAStatedOutcome(t *testing.T) {
	t.Parallel()
	transient := errors.New("transient write failure")
	quick := workflow.Settlement{Tries: 3, Wait: time.Millisecond}
	cases := []struct {
		name         string
		finish       []error // one per try; the last repeats
		fallback     error
		wantTries    int
		wantFallback bool
		wantFellBack bool
		wantErr      error
	}{
		{name: "recorded at once", finish: []error{nil}, wantTries: 1},
		{name: "recorded on a retry", finish: []error{transient, nil}, wantTries: 2},
		// A try that committed but reported failure: the retry meets the
		// fence, and nothing overwrites what was recorded.
		{name: "fenced after a committed try", finish: []error{transient, fmt.Errorf("step 1: %w", workflow.ErrStepAttemptLost)}, wantTries: 2, wantErr: workflow.ErrStepAttemptLost},
		{name: "never recorded: an error says so", finish: []error{transient}, wantTries: 3, wantFallback: true, wantFellBack: true},
		{name: "not even the error recorded: left for startup repair", finish: []error{transient}, fallback: transient, wantTries: 3, wantFallback: true, wantErr: transient},
		{name: "lost before the error is recorded", finish: []error{transient}, fallback: workflow.ErrStepAttemptLost, wantTries: 3, wantFallback: true, wantErr: workflow.ErrStepAttemptLost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tries, fallbacks := 0, 0
			var reason string
			fellBack, err := settleProduced(context.Background(), quick, idstest.N(1), func() error {
				e := tc.finish[min(tries, len(tc.finish)-1)]
				tries++
				return e
			}, func(r string) error {
				fallbacks++
				reason = r
				return tc.fallback
			})
			if tries != tc.wantTries || (fallbacks > 0) != tc.wantFallback || fellBack != tc.wantFellBack {
				t.Fatalf("tries %d fallbacks %d fellBack %v; want tries %d fallback %v fellBack %v", tries, fallbacks, fellBack, tc.wantTries, tc.wantFallback, tc.wantFellBack)
			}
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("err %v, want %v", err, tc.wantErr)
			}
			if tc.wantFallback && reason != "the step's work finished, but its result could not be recorded: transient write failure" {
				t.Fatalf("fallback reason %q", reason)
			}
			// A lost attempt is final: the fallback is not tried again.
			if errors.Is(tc.fallback, workflow.ErrStepAttemptLost) && fallbacks != 1 {
				t.Fatalf("fallback tried %d times after the attempt was lost", fallbacks)
			}
		})
	}
}
