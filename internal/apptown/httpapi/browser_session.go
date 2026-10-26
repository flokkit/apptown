package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/identity"
)

// The browser session cookie's names. The __Host- form binds the cookie to
// this exact origin over HTTPS; the plain name is for explicit HTTP
// development deployments.
const (
	SessionCookie       = "apptown_session"
	SecureSessionCookie = "__Host-apptown_session"
)

// BrowserSession is this instance's public origin and how it writes a signed-in
// browser's session cookie, from configuration fixed at startup. Origin is
// the one answer to "what is this AppTown's address": the browser origin
// check, the session cookie, invite and password reset links, and OAuth
// discovery all use it, so they cannot disagree.
type BrowserSession struct {
	PublicOrigin string // the whole origin -public-host stated ("https://forge.example"); when set it decides
	PublicHost   string // the configured public host; "" uses the request's
	PublicHTTPS  bool   // the tier domains are https: for a bare -public-host, the old sign of https
}

// Secure reports whether the browser reaches AppTown over HTTPS: as the stated
// origin says, else when the request came over TLS or a bare public host has
// https tier domains. Forwarded headers never decide it: a development proxy
// sends X-Forwarded-Proto: https to a browser on plain http. An AppTown behind
// a TLS proxy states its origin (-public-host https://...).
func (b BrowserSession) Secure(r *http.Request) bool {
	if b.PublicOrigin != "" {
		return strings.HasPrefix(b.PublicOrigin, "https://")
	}
	return r.TLS != nil || (b.PublicHost != "" && b.PublicHTTPS)
}

// Origin is the scheme and host a browser uses to reach AppTown.
func (b BrowserSession) Origin(r *http.Request) string {
	// A stated origin is PublicHost with the scheme Secure reports.
	host, scheme := r.Host, "http"
	if b.PublicHost != "" {
		host = b.PublicHost
	}
	if b.Secure(r) {
		scheme = "https"
	}
	return scheme + "://" + host
}

// CookieName is the session cookie's name for this request.
func (b BrowserSession) CookieName(r *http.Request) string {
	if b.Secure(r) {
		return SecureSessionCookie
	}
	return SessionCookie
}

// Start hands the browser a committed session.
func (b BrowserSession) Start(w http.ResponseWriter, r *http.Request, token string) {
	b.StartFor(w, r, token, identity.SessionTTL)
}

// StartFor gives a committed session its owner-selected browser lifetime.
func (b BrowserSession) StartFor(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: b.CookieName(r), Value: token, Path: "/", HttpOnly: true, Secure: b.Secure(r),
		SameSite: http.SameSiteLaxMode, MaxAge: int(ttl.Seconds()),
	})
}
