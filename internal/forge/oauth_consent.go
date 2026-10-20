package forge

import (
	"encoding/hex"
	"net/http"

	"flokkit.io/forge/internal/forge/identity"
)

// Consent is a one-use server record binding the exact displayed request to
// the current browser session. PKCE and the client's state parameter cannot
// provide this protection: a hostile client can choose both of those itself.
// Internal auth uses the login session; SSO uses a separate host-only browser
// nonce plus the authenticated username (we never receive the IdP session).
func (a *App) consentBrowser(w http.ResponseWriter, r *http.Request, create bool) string {
	if a.cfg.authMode == "internal" {
		if c, err := r.Cookie(a.sessionCookieName(r)); err == nil {
			return identity.HashToken("session:" + c.Value)
		}
		return ""
	}
	name := "apptown_oauth_browser"
	if a.secureBrowser(r) {
		name = "__Host-" + name
	}
	if c, err := r.Cookie(name); err == nil {
		if b, err := hex.DecodeString(c.Value); err == nil && len(b) == 32 {
			return identity.HashToken("proxy:" + c.Value)
		}
	}
	if !create {
		return ""
	}
	nonce := identity.RandomHex(32)
	http.SetCookie(w, &http.Cookie{Name: name, Value: nonce, Path: "/", HttpOnly: true,
		Secure: a.secureBrowser(r), SameSite: http.SameSiteLaxMode, MaxAge: int(identity.ConsentTTL.Seconds())})
	return identity.HashToken("proxy:" + nonce)
}
