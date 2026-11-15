package identity

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// StandingIssuerFacts is one person's current facts for a standing eligibility
// set. It grants no authority; Allowed is evaluated separately for each permission.
type StandingIssuerFacts struct {
	Actor   access.Actor
	Grants  []access.Grant
	Groups  []string
	Allowed []error
}

// StandingIssuerAllowsIn evaluates all supplied purposes from one name, enabled
// state, grant and membership observation. Nothing survives the operation.
// It creates no caller, credential or executable grant. The workflow owner
// separately checks the saved revision, scope and approval policy; the original
// human consent was admitted under that human's real credential bounds.
func (s *Service) StandingIssuerAllowsIn(ctx context.Context, q database.Queryer, issuer ids.ID, permissions ...access.Permission) (StandingIssuerFacts, error) {
	var out StandingIssuerFacts
	for _, p := range permissions {
		switch p.Capability {
		case access.CapChangeApprove, access.CapChangeLand, access.CapDeploymentDeploy, access.CapReleasePromote, access.CapStandardsPacks:
		default:
			return out, fmt.Errorf("unsupported standing purpose %q", p.Capability)
		}
	}
	if issuer.IsZero() {
		return out, access.ErrPrincipalRequired
	}
	row, err := queries(q).UserByPrincipal(ctx, issuer)
	found, err := database.Lookup(err)
	if err != nil {
		return out, err
	}
	if !found || row.Username == "" {
		return out, access.ErrPrincipalRequired
	}
	if row.DisabledAt.Valid {
		return out, &access.Denied{Code: access.DenyAccountDisabled, Reason: row.Username + "'s account is disabled or does not exist"}
	}
	pr := access.Principal{ID: issuer, Name: row.Username, Kind: "user"}
	facts := &serviceFacts{ctx: ctx, q: q, at: s.now(), disabled: map[string]bool{factKey(pr): false}}
	out.Actor = access.Actor{Principal: issuer, Name: row.Username}
	out.Grants = facts.Grants(pr)
	if facts.err != nil {
		return out, facts.err
	}
	out.Groups, err = PrincipalGroupsIn(ctx, q, issuer, facts.at)
	if err != nil {
		return out, err
	}
	for _, p := range permissions {
		dec, err := explainWithFacts(ctx, pr, access.Channel{Kind: "full-breadth"}, p, facts)
		if err != nil {
			return out, err
		}
		var denied error
		if !dec.Allow {
			denied = &access.Denied{Code: dec.Code, Reason: dec.Reason}
		}
		out.Allowed = append(out.Allowed, denied)
	}
	return out, nil
}
