package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
)

// Credential references (internal/forge/operations, "Authority"). A
// long operation keeps, with the principal that asked for it, a
// non-secret reference to the credential behind the request, so it can
// ask before each effect whether that credential is still live. No bearer
// token or hash is kept.
func TokenCredential(name string) string    { return "token:" + name }
func SessionCredential(id ids.ID) string    { return "session:" + id.String() }
func DelegationCredential(id ids.ID) string { return "oauth:" + id.String() }
func HeaderCredential(user string) string   { return "header:" + user }

// ErrCredentialGone is a captured credential that no longer stands.
var ErrCredentialGone = errors.New("the credential that asked for this no longer stands")

// RequireStill checks a captured principal again: its credential is still
// live (a token not revoked, a session not logged out or expired, a
// delegation not withdrawn), the account is still enabled, its current
// bounds are within the captured ones, and current grants allow p. The
// check is recorded like any consequential decision. A missing or
// unresolvable reference refuses.
//
// A request a trusted proxy or open mode attributed (header:<user>) had no
// credential the forge can revoke; it stands while the account is enabled
// and its grants allow. The forge acting for itself (system:<binding>)
// is not a person's authority to withdraw.
func (s *Service) RequireStill(ctx context.Context, captured access.Principal, p access.Permission) error {
	current, err := s.stillPrincipal(ctx, captured)
	if err != nil {
		return err
	}
	if current.Kind == "instrument" && strings.HasPrefix(current.Credential, "system:binding/") {
		return s.bindingAllows(ctx, strings.TrimPrefix(current.Credential, "system:"), p)
	}
	if current.Kind == "instrument" && strings.HasPrefix(current.Credential, "system:") {
		return nil
	}
	_, err = s.Require(ctx, current, p)
	return err
}

func (s *Service) stillPrincipal(ctx context.Context, c access.Principal) (access.Principal, error) {
	kind, ref, ok := strings.Cut(c.Credential, ":")
	if !ok || ref == "" || c.Name == "" {
		return access.Principal{}, fmt.Errorf("%w: no credential was recorded", ErrCredentialGone)
	}
	now := s.now()
	gone := func(why string, args ...any) (access.Principal, error) {
		return access.Principal{}, fmt.Errorf("%w: %s", ErrCredentialGone, fmt.Sprintf(why, args...))
	}
	if kind == "system" {
		if c.Kind != "instrument" || c.Instrument != "forge" {
			return gone("a system binding names no person")
		}
		return c, nil
	}
	// The account that asked: by its principal when it was admitted with
	// one (a rename keeps it; a later account with the old name is someone
	// else), by name for what was captured without.
	account := func() (*Account, error) {
		var a *Account
		var err error
		if c.ID.IsZero() {
			a, err = s.directory.login(ctx, c.Name)
		} else {
			a, err = s.directory.loginPrincipal(ctx, c.ID)
		}
		if err != nil {
			return nil, err
		}
		if a == nil {
			return nil, fmt.Errorf("%w: the account %s is gone", ErrCredentialGone, c.Name)
		}
		if err := a.admit(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCredentialGone, err)
		}
		return a, nil
	}
	switch kind {
	case "token":
		row, err := queries(s.db).AuthTokenByName(ctx, ref)
		found, err := database.Lookup(err)
		if err != nil {
			return access.Principal{}, err
		}
		if !found || !token(row).live() {
			return gone("the token %q was revoked", ref)
		}
		if !samePerson(c, row.PrincipalID, row.Username) || row.Purpose != c.Purpose {
			return gone("the token %q is not the one that asked", ref)
		}
		a, err := account()
		if err != nil {
			return access.Principal{}, err
		}
		pr := a.tokenPrincipal(row.Role, row.Purpose)
		if c.Role != "admin" {
			pr.Role = "member" // never wider than at admission
		}
		pr.Credential = c.Credential
		return pr, nil
	case "session":
		id, err := ids.Parse(ref)
		if err != nil || id.IsZero() {
			return gone("%q names no session", ref)
		}
		row, err := queries(s.db).SessionByID(ctx, id)
		found, err := database.Lookup(err)
		if err != nil {
			return access.Principal{}, err
		}
		if !found || !row.ExpiresAt.After(now) {
			return gone("the browser session that asked has ended (signed out or expired)")
		}
		if !samePerson(c, row.PrincipalID, row.Username) {
			return gone("the session is not the one that asked")
		}
		a, err := account()
		if err != nil {
			return access.Principal{}, err
		}
		pr := a.principal("session")
		pr.Credential = c.Credential
		return pr, nil
	case "oauth":
		id, err := ids.Parse(ref)
		if err != nil || id.IsZero() {
			return gone("%q names no connection", ref)
		}
		d, err := s.oauth.Connection(ctx, id)
		if err != nil {
			return access.Principal{}, err
		}
		if d == nil || !d.LiveAt(now) {
			return gone("the connection that asked was withdrawn")
		}
		a, err := account()
		if err != nil {
			return access.Principal{}, err
		}
		// Bounds intersects the connection's current scopes with those
		// captured: narrowing applies, widening does not.
		base := a.principal("oauth")
		base.DelegationID, base.Scopes = c.DelegationID, c.Scopes
		pr := d.Bounds(base, now)
		if pr.Name == "" {
			return gone("the connection is not the one that asked")
		}
		pr.Credential = c.Credential
		return pr, nil
	case "header":
		if c.ID.IsZero() && ref != c.Name {
			return gone("the attribution is not the one that asked")
		}
		a, err := account()
		if err != nil {
			return access.Principal{}, err
		}
		pr := a.principal(c.Via)
		pr.Credential = c.Credential
		return pr, nil
	}
	return gone("%q is not a credential AppTown can check", kind)
}

// samePerson is whether a credential's owner is the person who asked: by
// principal when the caller was admitted with one, else by name.
func samePerson(c access.Principal, owner ids.ID, ownerName string) bool {
	if !c.ID.IsZero() {
		return owner == c.ID
	}
	return ownerName == c.Name
}
