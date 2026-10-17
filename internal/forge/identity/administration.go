package identity

import (
	"context"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/identity/internal/accesssql"

	"flokkit.io/forge/internal/database"
)

func (a Account) administratorFor(pr access.Principal) bool {
	return a.Role == "admin" && (pr.Via != "token" || pr.Role == "admin")
}

// requireAdministration checks current root grants within the mutation.
// Generic identity capabilities cannot substitute for root authority.
// Principals are supplied by trusted protocol adapters, never command payloads.
// Grants are judged live as of now, the operation's clock.
func (s *Service) requireAdministration(ctx context.Context, q database.Queryer, pr access.Principal, cap access.Capability, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if pr.Name == "" {
		return access.ErrPrincipalRequired
	}
	open := s.cfg.OpenInstance
	denied := &access.Denied{Code: access.DenyMissingCapability, Reason: "admin role required"}
	if pr.Kind != "user" {
		return denied
	}
	switch pr.Via {
	case "open":
		if !open {
			return denied
		}
	case "session", "proxy", "token", "oauth":
	default:
		return access.ErrPrincipalRequired
	}
	// The same agent bound access.Decide applies: an OAuth delegation or an
	// agent token administers nothing, so it cannot mint itself a wider
	// credential here.
	if !access.CredentialCovers(pr, cap) {
		return &access.Denied{Code: access.DenyDelegationNarrow, Reason: access.CredentialNarrowReason(pr, "administer this resource")}
	}
	// The admitted account by its principal: a rename keeps it, and a later
	// holder of its old name is someone else. A caller named only (open or
	// header modes before admission recorded principals) by its name.
	var row accesssql.User
	var err error
	if pr.ID.IsZero() {
		row, err = queries(q).UserByName(ctx, pr.Name)
	} else {
		row, err = queries(q).UserByPrincipal(ctx, pr.ID)
	}
	found, err := database.Lookup(err)
	if err != nil {
		return err
	}
	if found {
		a, err := accountIn(ctx, q, row, now)
		if err != nil {
			return err
		}
		if err := a.admit(); err != nil {
			return &access.Denied{Code: access.DenyAccountDisabled, Reason: err.Error()}
		}
		if pr.Kind == "user" && a.administratorFor(pr) {
			return nil
		}
	}
	return denied
}

// Administrative decisions and mutations share the existing account lock.
// An account cannot be demoted or disabled between this check and the write.
// The operation reads the forge's clock once and gives apply that time.
func (s *Service) administer(ctx context.Context, pr access.Principal, cap access.Capability, apply func(tx *database.Tx, now time.Time) error) error {
	if pr.Name == "" {
		return access.ErrPrincipalRequired
	}
	now := s.now()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		return preserveRoot(ctx, tx, now, func() error {
			if err := s.requireAdministration(ctx, tx, pr, cap, now); err != nil {
				return err
			}
			return apply(tx, now)
		})
	})
}
