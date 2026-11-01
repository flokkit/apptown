package remediation

import (
	"context"
	"fmt"
	"sync"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/conformance"
)

// Review is what the post-landing review said about the item's check.
type Review struct {
	ID     ids.ID
	State  conformance.ObservationState
	Detail string
}

type verificationRecords interface {
	VerificationCurrent(context.Context, Item) (bool, error)
	Record(context.Context, Item, Outcome) (*Item, error)
}

// Reviews run outside transactions. Only a result for the current observation
// may commit. Coalesce simultaneous requests in this process; after restart,
// a verifying item is still discoverable by ordinary reconciliation.
type verifier struct {
	records verificationRecords
	review  func(context.Context, Item) Review
	mu      sync.Mutex
	active  map[ids.ID]bool
}

func newVerifier(records verificationRecords, review func(context.Context, Item) Review) *verifier {
	return &verifier{records: records, review: review}
}

func (v *verifier) Verify(ctx context.Context, item Item) error {
	v.mu.Lock()
	if v.active[item.ID] {
		v.mu.Unlock()
		return nil
	}
	if v.active == nil {
		v.active = make(map[ids.ID]bool)
	}
	v.active[item.ID] = true
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		delete(v.active, item.ID)
		v.mu.Unlock()
	}()
	if current, err := v.records.VerificationCurrent(ctx, item); err != nil || !current {
		return err
	}
	review := v.review(ctx, item)
	switch review.State {
	case conformance.Conforms, conformance.Excepted, conformance.DoesNotConform:
	default:
		// A review the caller stopped judged nothing: the item stays
		// verifying, for the next pass to review again.
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	result := Outcome{Kind: "verified", ReviewID: review.ID, Verdict: review.State}
	switch review.State {
	case conformance.Conforms, conformance.Excepted:
	case conformance.DoesNotConform:
		result.Reason = fmt.Sprintf("landed as change %d, but the check still fails: %s", item.ChangeID, review.Detail)
	default:
		result.Reason = fmt.Sprintf("landed as change %d; the check could not be judged afterwards (%s): %s", item.ChangeID, review.State, review.Detail)
	}
	_, err := v.records.Record(ctx, item, result)
	return err
}
