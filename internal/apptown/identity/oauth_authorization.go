package identity

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/strs"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/stamp"
)

const ConsentTTL = 10 * time.Minute

// AuthorizationRequest is also the canonical consent binding. Keep these field
// names and order: outstanding requests use this encoding in the database.
type AuthorizationRequest struct {
	ClientID, RedirectURI, Scope, State, Challenge, Method, Resource string
}

type Registration struct {
	Name         string
	RedirectURIs []string
}
type authorizationView struct {
	Client oauthClient
	Scopes []string
}

// AuthorizationProblem is a refusal, not a storage failure. SafeRedirect is set
// only after the client and its exact registered redirect have been verified.
type AuthorizationProblem struct {
	Reason       string
	SafeRedirect bool
}

func (e *AuthorizationProblem) Error() string { return e.Reason }

type RegistrationProblem struct {
	Reason string
}

func (e *RegistrationProblem) Error() string { return e.Reason }

func (in Registration) validate() error {
	if len(in.RedirectURIs) == 0 {
		return &RegistrationProblem{Reason: "client_name and at least one redirect_uri are required"}
	}
	for _, raw := range in.RedirectURIs {
		if !redirectURIAllowed(raw) {
			return &RegistrationProblem{Reason: fmt.Sprintf("redirect_uri %q must be https, a loopback http address, or a custom scheme", raw)}
		}
	}
	return nil
}
func redirectURIAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.Host != ""
	case "http":
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	}
	return true
}
func (s *Service) RegisterOAuthClient(ctx context.Context, in Registration) (oauthClient, error) {
	if err := in.validate(); err != nil {
		return oauthClient{}, err
	}
	return s.oauth.registerClient(ctx, in.Name, in.RedirectURIs)
}

func (c oauthClient) inspect(in AuthorizationRequest) (authorizationView, error) {
	if !slices.Contains(c.RedirectURIs, in.RedirectURI) {
		return authorizationView{}, &AuthorizationProblem{Reason: "redirect_uri is not one this client registered"}
	}
	if in.Method != "S256" || in.Challenge == "" {
		return authorizationView{}, &AuthorizationProblem{Reason: "code_challenge with S256 is required (PKCE)", SafeRedirect: true}
	}
	scopes := strings.Fields(in.Scope)
	for _, scope := range scopes {
		if access.ScopeMeans(scope) == "" {
			return authorizationView{}, &AuthorizationProblem{Reason: fmt.Sprintf("unknown scope %q", scope), SafeRedirect: true}
		}
	}
	if len(scopes) == 0 {
		scopes = []string{"apptown:user:read"}
	}
	return authorizationView{Client: c, Scopes: scopes}, nil
}
func (s *Service) InspectAuthorization(ctx context.Context, in AuthorizationRequest) (authorizationView, error) {
	c, err := s.oauth.clientByID(ctx, in.ClientID)
	// Preserve the browser door's existing unavailable-client response.
	if err != nil {
		log.Printf("read failed: %v", err)
	}
	if c == nil {
		return authorizationView{}, &AuthorizationProblem{Reason: "unknown client_id; the agent must register first (POST /oauth/register)"}
	}
	return c.inspect(in)
}

// BrowserAuthorization comes from the admitted browser adapter, never a client
// form's asserted username. The adapter owns session/proxy and network admission.
type BrowserAuthorization struct {
	User, Binding string
	Request       AuthorizationRequest
}
type preparedConsent struct {
	Token string
	View  authorizationView
}
type ConsentChoice string

const (
	ConsentAllow ConsentChoice = "allow"
	ConsentDeny  ConsentChoice = "deny"
)

type ConsentDecision struct {
	Browser BrowserAuthorization
	Token   string
	Choice  ConsentChoice
}
type consentResult struct {
	Code   string
	Choice ConsentChoice
}

type consentFailure uint8

const (
	ConsentNoBrowser consentFailure = iota
	ConsentBadChoice
	ConsentChanged
	consentRecordFailed
	consentClaimFailed
	ConsentIssueFailed
)

type ConsentError struct {
	Failure consentFailure
	Cause   error
}

func (e *ConsentError) Unwrap() error { return e.Cause }
func (e *ConsentError) Error() string {
	switch e.Failure {
	case ConsentNoBrowser:
		return "sign in again to authorize this connection"
	case ConsentBadChoice:
		return "choose Allow or Cancel on the consent page"
	case ConsentChanged:
		return "this consent request expired or changed; reopen the connection request"
	case consentRecordFailed:
		return fmt.Sprintf("recording the consent request: %v", e.Cause)
	case consentClaimFailed:
		return "could not consume the consent request"
	default:
		return fmt.Sprintf("could not record the authorization: %v", e.Cause)
	}
}
func (s *Service) PrepareConsent(ctx context.Context, in BrowserAuthorization) (preparedConsent, error) {
	view, err := s.InspectAuthorization(ctx, in.Request)
	if err != nil {
		return preparedConsent{}, err
	}
	if in.User == "" || in.Binding == "" {
		return preparedConsent{}, &ConsentError{Failure: ConsentNoBrowser}
	}
	token := RandomHex(32)
	request, err := strs.JSON(in.Request)
	if err != nil {
		return preparedConsent{}, &ConsentError{Failure: consentRecordFailed, Cause: err}
	}
	if err := s.oauth.recordConsent(ctx, HashToken(token), in.User, in.Binding, request, ConsentTTL); err != nil {
		return preparedConsent{}, &ConsentError{Failure: consentRecordFailed, Cause: err}
	}
	return preparedConsent{Token: token, View: view}, nil
}
func (s *Service) DecideConsent(ctx context.Context, in ConsentDecision) (consentResult, error) {
	b := in.Browser
	view, err := s.InspectAuthorization(ctx, b.Request)
	if err != nil {
		return consentResult{}, err
	}
	if in.Choice != ConsentAllow && in.Choice != ConsentDeny {
		return consentResult{}, &ConsentError{Failure: ConsentBadChoice}
	}
	if b.User == "" {
		return consentResult{}, &ConsentError{Failure: ConsentChanged}
	}
	request, err := strs.JSON(b.Request)
	if err != nil {
		return consentResult{}, &ConsentError{Failure: consentClaimFailed, Cause: err}
	}
	consumed, err := s.oauth.consumeConsent(ctx, HashToken(in.Token), b.User, b.Binding, request)
	if err != nil {
		return consentResult{}, &ConsentError{Failure: consentClaimFailed, Cause: err}
	}
	if !consumed {
		return consentResult{}, &ConsentError{Failure: ConsentChanged}
	}
	if in.Choice == ConsentDeny {
		// Denials have always used a best-effort event, after the one-use claim.
		if body, err := strs.JSON(map[string]string{"clientId": view.Client.ID}); err != nil {
			log.Printf("best-effort write failed: %v", err)
		} else if err := audit.Append(ctx, s.db, stamp.Of(s.now()).String(), b.User, ids.Nil, "oauth.denied", view.Client.Name, body); err != nil {
			log.Printf("best-effort write failed: %v", err)
		}
		return consentResult{Choice: ConsentDeny}, nil
	}
	code, err := s.oauth.issueCode(ctx, view.Client, b.User, b.Request.RedirectURI, view.Scopes, b.Request.Resource, b.Request.Challenge)
	if err != nil {
		return consentResult{}, &ConsentError{Failure: ConsentIssueFailed, Cause: err}
	}
	return consentResult{Choice: ConsentAllow, Code: code}, nil
}
