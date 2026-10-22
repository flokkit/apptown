package apptown

import (
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/httpapi"
)

// Browser security belongs inside VHost: hosted applications have their own
// origins and policy. Neither their origin nor a forwarded client header may
// grant browser authority on AppTown. Headerless CLI/provider calls still pass
// CrossOriginProtection and must authenticate at their normal door.
func (a *App) browserSecurity(next http.Handler) http.Handler {
	csrf := http.NewCrossOriginProtection()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'self'; object-src 'none'")
		w.Header().Set("Referrer-Policy", "same-origin")
		// An invite or password reset link carries its token in the path;
		// the page it opens sends no referrer at all.
		if strings.HasPrefix(r.URL.Path, "/invite/") {
			w.Header().Set("Referrer-Policy", "no-referrer")
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			// The standard check also rejects same-site sibling origins using
			// Fetch Metadata. When Origin is present, additionally require the
			// exact scheme/host/port rather than just the Host comparison.
			if err := csrf.Check(r); err != nil || (len(r.Header.Values("Origin")) != 0 && !a.sameBrowserOrigin(r)) {
				writeErr(w, http.StatusForbidden, "open AppTown directly to make this request")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) secureBrowser(r *http.Request) bool { return a.cfg.browserSession().Secure(r) }

func (a *App) browserOrigin(r *http.Request) string { return a.cfg.browserSession().Origin(r) }

func (a *App) sameBrowserOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	return len(origins) == 1 && origins[0] == a.browserOrigin(r)
}

const secureSessionCookie = httpapi.SecureSessionCookie

func (a *App) sessionCookieName(r *http.Request) string { return a.cfg.browserSession().CookieName(r) }

// browserSession is the public origin and session cookie a browser is given.
func (c configuration) browserSession() httpapi.BrowserSession {
	return httpapi.BrowserSession{PublicOrigin: c.publicOrigin(), PublicHost: c.publicHost, PublicHTTPS: c.routing().Scheme == "https"}
}
