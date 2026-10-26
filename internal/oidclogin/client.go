// Package oidclogin verifies external authentication evidence. It knows no
// AppTown accounts or grants; callers own browser transactions and admission.
package oidclogin

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/flokkit/apptown/internal/clock"
	"golang.org/x/oauth2"
)

var ErrConfiguration = errors.New("external sign-in configuration is invalid")
var ErrUnavailable = errors.New("the external sign-in provider could not be reached or read")
var ErrRejected = errors.New("external sign-in evidence was refused; start sign-in again")

// Configuration is an administrator-selected connection snapshot, never a
// callback's issuer or endpoints. Secret is used for this exchange only.
type Configuration struct{ Issuer, ClientID, Secret, Callback string }

// Evidence is signed provider identity, not permission or an account name.
type Evidence struct {
	Issuer, Subject string
	AuthenticatedAt time.Time
}

// Client confines discovery, exchange and JWKS to bounded HTTPS requests.
type Client struct {
	http     *http.Client
	now      func() time.Time
	loopback bool
}

// New uses system TLS trust. An optional transport can supply explicitly
// configured trust roots. Development permits HTTP only on loopback hosts.
func New(transport http.RoundTripper, now func() time.Time, development bool) *Client {
	if transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
		t.TLSHandshakeTimeout = 5 * time.Second
		t.ResponseHeaderTimeout = 5 * time.Second
		transport = t
	}
	now = clock.Or(now)
	c := &Client{now: now, loopback: development}
	c.http = &http.Client{Transport: boundedTransport{base: transport, loopback: development}, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRejected }}
	return c
}
func validURL(raw string, loopback bool) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return loopback && u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()))
}

// Validate checks configuration without contacting a provider.
func (c *Client) Validate(cfg Configuration) error {
	if cfg.ClientID == "" || !validURL(cfg.Issuer, c.loopback) || !validURL(cfg.Callback, c.loopback) {
		return ErrConfiguration
	}
	return nil
}
func (c *Client) discover(ctx context.Context, cfg Configuration) (*oidc.Provider, oauth2.Config, error) {
	if c.Validate(cfg) != nil {
		return nil, oauth2.Config{}, ErrConfiguration
	}
	p, err := oidc.NewProvider(oidc.ClientContext(ctx, c.http), cfg.Issuer)
	if err != nil {
		return nil, oauth2.Config{}, ErrUnavailable
	}
	endpoint := p.Endpoint()
	if !validURL(endpoint.AuthURL, c.loopback) || !validURL(endpoint.TokenURL, c.loopback) {
		return nil, oauth2.Config{}, ErrConfiguration
	}
	var metadata struct {
		AuthMethods []string `json:"token_endpoint_auth_methods_supported"`
	}
	if p.Claims(&metadata) != nil {
		return nil, oauth2.Config{}, ErrConfiguration
	}
	// OIDC discovery defaults to client_secret_basic. Never let oauth2 probe
	// alternate authentication styles by repeating a one-use code exchange.
	endpoint.AuthStyle = oauth2.AuthStyleInHeader
	if len(metadata.AuthMethods) > 0 {
		supported := false
		for _, m := range metadata.AuthMethods {
			if m == "client_secret_basic" {
				supported = true
				break
			}
		}
		if !supported {
			for _, m := range metadata.AuthMethods {
				if m == "client_secret_post" {
					endpoint.AuthStyle = oauth2.AuthStyleInParams
					supported = true
					break
				}
			}
		}
		if !supported {
			return nil, oauth2.Config{}, ErrConfiguration
		}
	}
	return p, oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.Secret, RedirectURL: cfg.Callback, Endpoint: endpoint, Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}, nil
}

// Inspect observes discovery; it neither proves a user login nor activates it.
func (c *Client) Inspect(ctx context.Context, cfg Configuration) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, _, err := c.discover(ctx, cfg)
	return err
}

// Begin returns a redirect using caller-owned random state, nonce and verifier.
func (c *Client) Begin(ctx context.Context, cfg Configuration, state, nonce, verifier string, fresh bool) (string, error) {
	if state == "" || nonce == "" || len(verifier) < 43 {
		return "", ErrConfiguration
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, o, err := c.discover(ctx, cfg)
	if err != nil {
		return "", err
	}
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)}
	if fresh {
		opts = append(opts, oauth2.SetAuthURLParam("max_age", "300"))
	}
	return o.AuthCodeURL(state, opts...), nil
}

// Complete exchanges one authorization code and verifies signed identity. The
// caller must have consumed its browser-bound transaction before invoking it.
func (c *Client) Complete(ctx context.Context, cfg Configuration, code, nonce, verifier string, fresh bool) (Evidence, error) {
	if code == "" || nonce == "" || len(verifier) < 43 {
		return Evidence{}, ErrRejected
	}
	var unavailable atomic.Bool
	local := *c
	httpClient := *c.http
	httpClient.Transport = observedTransport{base: c.http.Transport, failed: &unavailable}
	local.http = &httpClient
	c = &local
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	p, o, err := c.discover(ctx, cfg)
	if err != nil {
		return Evidence{}, err
	}
	ctx = oidc.ClientContext(ctx, c.http)
	token, err := o.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		if ctx.Err() != nil {
			return Evidence{}, ErrUnavailable
		}
		var r *oauth2.RetrieveError
		if errors.As(err, &r) && r.Response != nil && r.Response.StatusCode < 500 {
			return Evidence{}, ErrRejected
		}
		return Evidence{}, ErrUnavailable
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		return Evidence{}, ErrRejected
	}
	checked, err := p.VerifierContext(ctx, &oidc.Config{ClientID: cfg.ClientID, Now: c.now}).Verify(ctx, raw)
	if err != nil {
		if ctx.Err() != nil || unavailable.Load() {
			return Evidence{}, ErrUnavailable
		}
		return Evidence{}, ErrRejected
	}
	if checked.Nonce != nonce || strings.TrimSpace(checked.Subject) == "" {
		return Evidence{}, ErrRejected
	}
	var claims struct {
		AuthorizedParty string `json:"azp"`
		AuthTime        int64  `json:"auth_time"`
	}
	if checked.Claims(&claims) != nil || (claims.AuthorizedParty != "" && claims.AuthorizedParty != cfg.ClientID) || (len(checked.Audience) > 1 && claims.AuthorizedParty != cfg.ClientID) {
		return Evidence{}, ErrRejected
	}
	e := Evidence{Issuer: cfg.Issuer, Subject: checked.Subject}
	if claims.AuthTime > 0 {
		e.AuthenticatedAt = time.Unix(claims.AuthTime, 0)
	}
	if fresh && (e.AuthenticatedAt.IsZero() || e.AuthenticatedAt.Before(c.now().Add(-5*time.Minute)) || e.AuthenticatedAt.After(c.now().Add(30*time.Second))) {
		return Evidence{}, ErrRejected
	}
	return e, nil
}

type boundedTransport struct {
	base     http.RoundTripper
	loopback bool
}

func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !validURL(r.URL.String(), t.loopback) {
		return nil, ErrConfiguration
	}
	out, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if out.ContentLength > 1<<20 {
		out.Body.Close()
		return nil, ErrUnavailable
	}
	out.Body = &boundedBody{ReadCloser: out.Body, left: 1 << 20}
	return out, nil
}

type boundedBody struct {
	io.ReadCloser
	left int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.left == 0 {
		var one [1]byte
		n, e := b.ReadCloser.Read(one[:])
		if n > 0 {
			return 0, ErrUnavailable
		}
		return 0, e
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, e := b.ReadCloser.Read(p)
	b.left -= int64(n)
	return n, e
}

// A failed JWKS read must not be reported as a bad identity. The upstream
// verifier formats fetch errors without preserving their Go error chain.
type observedTransport struct {
	base   http.RoundTripper
	failed *atomic.Bool
}

func (t observedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	out, e := t.base.RoundTrip(r)
	if e != nil {
		t.failed.Store(true)
		return nil, e
	}
	if out.StatusCode < 200 || out.StatusCode >= 300 {
		t.failed.Store(true)
	}
	out.Body = observedBody{ReadCloser: out.Body, failed: t.failed}
	return out, nil
}

type observedBody struct {
	io.ReadCloser
	failed *atomic.Bool
}

func (b observedBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	if e != nil && e != io.EOF {
		b.failed.Store(true)
	}
	return n, e
}
