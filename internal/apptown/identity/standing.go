package identity

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// StandingIssuerAllowsIn observes current person/grant eligibility for a saved
// standing purpose. It creates no caller/session, credential or executable grant.
// The workflow owner must separately prove enabled revision, captured scope and
// approval policy. Admission of the original consent uses the human's real
// credential bounds; this durable consent is not a continuation of that session.
func (s *Service) StandingIssuerAllowsIn(ctx context.Context, q database.Queryer, issuer ids.ID, p access.Permission) (access.Actor, error) {
	switch p.Capability {
	case access.CapChangeApprove, access.CapChangeLand, access.CapDeploymentDeploy, access.CapReleasePromote:
	default:
		return access.Actor{}, fmt.Errorf("unsupported standing purpose %q", p.Capability)
	}
	if issuer.IsZero() {
		return access.Actor{}, access.ErrPrincipalRequired
	}
	name, err := PersonNameIn(ctx, q, issuer)
	if err != nil {
		return access.Actor{}, err
	}
	if name == "" {
		return access.Actor{}, access.ErrPrincipalRequired
	}
	pr := access.Principal{ID: issuer, Name: name, Kind: "user"}
	if err = RequireEnabledIn(ctx, q, pr); err != nil {
		return access.Actor{}, err
	}
	dec, err := explainWithFacts(ctx, pr, access.Channel{Kind: "full-breadth"}, p, &serviceFacts{ctx: ctx, q: q, at: s.now()})
	if err != nil {
		return access.Actor{}, err
	}
	if !dec.Allow {
		return access.Actor{}, &access.Denied{Code: dec.Code, Reason: dec.Reason}
	}
	return access.Actor{Principal: issuer, Name: name}, nil
}
