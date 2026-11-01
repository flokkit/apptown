package identity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log"
	"time"
)

type CodeExchange struct{ Code, Verifier, ClientID, RedirectURI, AuthMethod string }

// CodeExchangeProblem is an invalid grant; database failures remain errors.
type CodeExchangeProblem struct{ Reason string }

func (e *CodeExchangeProblem) Error() string { return e.Reason }

var errCodeReplay = &CodeExchangeProblem{Reason: "this code was already used; the tokens it minted are revoked"}

type codeState uint8

const (
	codeAvailable codeState = iota
	codeUsed
	codeExpired
)

func (c authorizationCode) state(now time.Time) codeState {
	if c.Used.Valid {
		return codeUsed
	}
	if !c.Expires.After(now) {
		return codeExpired
	}
	return codeAvailable
}
func (c authorizationCode) checkExchange(in CodeExchange, now time.Time) error {
	switch c.state(now) {
	case codeUsed:
		return errCodeReplay
	case codeExpired:
		return &CodeExchangeProblem{Reason: "the code is expired, or the client or redirect_uri does not match"}
	}
	if c.ClientID != in.ClientID || c.Redirect != in.RedirectURI {
		return &CodeExchangeProblem{Reason: "the code is expired, or the client or redirect_uri does not match"}
	}
	sum := sha256.Sum256([]byte(in.Verifier))
	if in.Verifier == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != c.Challenge {
		return &CodeExchangeProblem{Reason: "PKCE verification failed"}
	}
	return nil
}

// ExchangeCode owns the whole exchange. A caller cannot substitute a code row
// or bypass validation before the one-success database claim.
func (s *Service) ExchangeCode(ctx context.Context, in CodeExchange) (Pair, error) {
	c, err := s.oauth.codeByValue(ctx, in.Code)
	if err != nil {
		return Pair{}, err
	}
	return s.exchangeCode(ctx, in, c)
}

func (s *Service) exchangeCode(ctx context.Context, in CodeExchange, c *authorizationCode) (Pair, error) {
	if c == nil {
		return Pair{}, &CodeExchangeProblem{Reason: "unknown code"}
	}
	if err := c.checkExchange(in, s.now()); err != nil {
		if errors.Is(err, errCodeReplay) {
			return Pair{}, s.oauth.replay(ctx, c)
		}
		return Pair{}, err
	}
	client, err := s.oauth.clientByID(ctx, in.ClientID)
	if err != nil {
		log.Printf("read failed: %v", err)
	}
	name := in.ClientID
	if client != nil {
		name = client.Name
	}
	_, pair, again, err := s.oauth.exchange(ctx, in.Code, c, name, in.AuthMethod)
	if err != nil {
		return Pair{}, err
	}
	if again {
		return Pair{}, s.oauth.replay(ctx, c)
	}
	pair.Scopes = append([]string(nil), c.Scopes...)
	return pair, nil
}
func (o *oauthStore) replay(ctx context.Context, c *authorizationCode) error {
	if err := o.revokeMintedBy(ctx, c); err != nil {
		log.Printf("revoking the tokens a replayed code minted: %v", err)
	}
	return errCodeReplay
}
