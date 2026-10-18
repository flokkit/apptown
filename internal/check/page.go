package check

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The front page: what a visitor who opens the app gets. A readiness path
// answering 200 says the process serves; it does not say anyone can use the
// app. Rails exempts /up from host authorization, so an app whose host list
// refused every real page can pass the boot, the deploy and the runtime
// check. The page probe asks the question a visitor does: GET / as it
// arrives through the front door.

// PageProbe is one front-page request: the address the app is asked at, and
// the host name and headers a visitor's request carries through the front
// door. An empty Host leaves the address's own.
type PageProbe struct {
	URL     string
	Host    string
	Headers map[string]string
}

// pageRedirects bounds the same-origin redirects a page probe follows: a
// root that sends a visitor to /login or /dashboard is judged by where it
// lands.
const pageRedirects = 5

// ProbeTransport is the transport every probe of an app uses: a pool of
// its own, so a boot's checks still reuse one connection, but one that
// nothing else in the process closes. A connection pooled on
// http.DefaultTransport is closed under the probe whenever anything calls
// CloseIdleConnections on it (every httptest.Server.Close does), and the
// probe read that as an app that gave no answer.
var ProbeTransport http.RoundTripper = http.DefaultTransport.(*http.Transport).Clone()

var pageClient = &http.Client{
	Transport:     ProbeTransport,
	Timeout:       8 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// ProbePage GETs the app's root as a visitor would and returns the status
// it ends on and the path that answered. Redirects on the same origin are
// followed (to the same address, with the same host and headers); one that
// leaves the origin (a sign-in provider) is the answer as it stands.
func ProbePage(ctx context.Context, p PageProbe) (status int, path string, err error) {
	base, err := url.Parse(strings.TrimRight(p.URL, "/"))
	if err != nil {
		return 0, "", err
	}
	origin := p.Host
	if origin == "" {
		origin = base.Host
	}
	path = "/"
	for i := 0; ; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String()+path, nil)
		if err != nil {
			return 0, path, err
		}
		for k, v := range p.Headers {
			req.Header.Set(k, v)
		}
		if p.Host != "" {
			req.Host = p.Host
		}
		resp, err := pageClient.Do(req)
		if err != nil {
			return 0, path, err
		}
		resp.Body.Close()
		if resp.StatusCode < 300 || resp.StatusCode >= 400 || i == pageRedirects {
			return resp.StatusCode, path, nil
		}
		loc, err := url.Parse(resp.Header.Get("Location"))
		if err != nil || (loc.Host != "" && !sameHost(loc.Host, origin)) {
			return resp.StatusCode, path, nil
		}
		next := loc.RequestURI()
		if !strings.HasPrefix(next, "/") {
			next = "/" + next
		}
		path = next
	}
}

func sameHost(a, b string) bool {
	host := func(h string) string {
		if name, _, err := net.SplitHostPort(h); err == nil {
			return name
		}
		return h
	}
	return strings.EqualFold(host(a), host(b))
}

// PageRefusal says why a front-page answer means a visitor cannot use the
// app, or "" when it does not: any server error, and the two ways a host
// allow-list refuses a host it was never told (Django's DisallowedHost is
// 400, Rails host authorization 403). Any other answer means the app
// answered: a 401 or a redirect to sign in is a working front page, and a
// 404 is an API-only app with nothing at its root (greeting-api,
// pulse-api).
func PageRefusal(status int, path string) string {
	switch {
	case status >= 500:
		return fmt.Sprintf("GET %s answered %d: the page a visitor opens fails with a server error — the app's log says why", path, status)
	case status == http.StatusBadRequest || status == http.StatusForbidden:
		return fmt.Sprintf("GET %s answered %d: a visitor who opens the app is refused. A host allow-list that does not take its hosts from FORGE_ORIGINS refuses exactly this way (Rails config.hosts answers 403, Django ALLOWED_HOSTS 400); the readiness path is usually exempt, so readiness passing does not show it", path, status)
	}
	return ""
}

// JudgePage asks the front page through the front door: refused is a
// finding, no answer is the review's inability to look, not the app's fault.
func JudgePage(ctx context.Context, frontDoor string) (Outcome, string) {
	status, path, err := ProbePage(ctx, PageProbe{URL: frontDoor})
	if err != nil {
		return CannotAssess, fmt.Sprintf("the front door %s did not answer the review: %v", frontDoor, err)
	}
	if why := PageRefusal(status, path); why != "" {
		return DoesNotConform, why + " (" + strings.TrimRight(frontDoor, "/") + path + ")"
	}
	return Conforms, fmt.Sprintf("front door %s%s → %d", strings.TrimRight(frontDoor, "/"), path, status)
}
