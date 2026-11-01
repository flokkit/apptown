package identity

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
)

var ErrOAuthCredential = errors.New("invalid OAuth credential")

type resolvedOAuth struct {
	Principal access.Principal
	ClientID  string
}
type credentialState uint8

const (
	credentialLive credentialState = iota
	credentialRevoked
	credentialExpired
)

func (t tokenRecord) state(now time.Time) credentialState {
	if t.Revoked {
		return credentialRevoked
	}
	if !t.Expires.After(now) {
		return credentialExpired
	}
	return credentialLive
}

// ResolveOAuth admits a token to its audience and applies the delegation's
// bounds to the current person. It does not authorize an action on a resource.
func (s *Service) ResolveOAuth(ctx context.Context, presented, audience string) (resolvedOAuth, error) {
	if !strings.HasPrefix(presented, AccessPrefix) {
		return resolvedOAuth{}, ErrOAuthCredential
	}
	token, err := s.oauth.token(ctx, presented, "access")
	if err != nil {
		return resolvedOAuth{}, err
	}
	now := s.now()
	if token == nil || token.state(now) != credentialLive {
		return resolvedOAuth{}, ErrOAuthCredential
	}
	d, err := s.oauth.connection(ctx, token.DelegationID)
	if err != nil {
		return resolvedOAuth{}, err
	}
	if !d.LiveAt(now) || d.Audience != audience {
		return resolvedOAuth{}, ErrOAuthCredential
	}
	a, err := s.observedAccount(ctx, d.User)
	if errors.Is(err, ErrAccountDisabled) {
		return resolvedOAuth{}, ErrOAuthCredential
	}
	if err != nil {
		return resolvedOAuth{}, err
	}
	// Last-use metadata is best effort; account facts above are required.
	if err := queries(s.db).TouchDelegation(ctx, accesssql.TouchDelegationParams{ID: d.ID, At: now.UTC()}); err != nil {
		log.Printf("delegation %d: stamping its use: %v", d.ID, err)
	}
	pr := d.bounds(a.principal(""), now)
	if pr.Name == "" {
		return resolvedOAuth{}, ErrOAuthCredential
	}
	pr.Credential = delegationCredential(d.ID)
	return resolvedOAuth{Principal: pr, ClientID: d.ClientID}, nil
}

// RevokeOAuthToken ends the connection represented by either member of a pair.
// An unrecognized token succeeds; a recognized connection settles atomically.
func (s *Service) RevokeOAuthToken(ctx context.Context, presented string) error {
	id, found, err := s.oauth.delegationOfToken(ctx, presented)
	if err != nil || !found {
		return err
	}
	err = s.oauth.revoke(ctx, id, "the client")
	if err != nil {
		log.Printf("delegation %d: revoking: %v", id, err)
	}
	return err
}
