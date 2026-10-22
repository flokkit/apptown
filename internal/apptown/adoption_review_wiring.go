package apptown

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/database"
)

// adoptionReviews is Adoption's review requester: a recorded change of a
// project's way of working admits that project's review through
// Verification's fleet, in the adoption's transaction. Conformance is built
// after Adoption (it reads adopted declarations through it), so the fleet is
// assembled when a revision asks, not when Adoption is.
type adoptionReviews struct {
	fleet func() *verification.Fleet
}

func (r adoptionReviews) RequestIn(ctx context.Context, tx *database.Tx, recorded adoption.Recorded) error {
	return r.fleet().RequestAdoptionReviewIn(ctx, tx, recorded)
}
