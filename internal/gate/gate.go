package gate

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"flokkit.io/forge/internal/clock"
)

const (
	// TokenHeader carries the gate secret on the forge's probes.
	TokenHeader = "Forge-Restore-Token"
	// CookieName is the gate's session cookie. The __Host- prefix makes the
	// browser refuse it unless it is Secure, has Path=/ and no Domain, so
	// no other site or subdomain can plant or widen it.
	CookieName = "__Host-apptown-gate"
	// RefusedHeader marks the gate's own refusals (RefusedValue), so the
	// forge's check of the gate can tell a refusal from an app that answers
	// 404 or 403 behind a gate that let the request through. The gate
	// strips it from every response the app sends, so the app cannot
	// produce it.
	RefusedHeader = "Forge-Gate"
	RefusedValue  = "refused"
	// EnterPath is where a person arrives with a ticket from the forge.
	EnterPath = "/_forge/enter"
	// RedeemPath is the forge endpoint that exchanges a ticket for a person
	// and a deadline.
	// It is outside /api/ on purpose: the gate presents its secret as a
	// bearer credential, which the forge's API would read as a person's
	// token; this path authenticates the gate itself.
	RedeemPath = "/gate/redeem"

	// enterRefused is shown for every failed entry. It does not say why:
	// telling an outsider whether a ticket existed, was used or expired
	// helps nobody but them.
	enterRefused = "This link was already used or has expired. Press Open again in AppTown.\n"

	redeemTimeout = 10 * time.Second
	// maxRedeemBody bounds how much of the forge's answer the gate reads.
	maxRedeemBody = 64 << 10
)

// Options are the gate's replaceable collaborators, for tests.
type Options struct {
	// Client redeems tickets with the forge. The default has a 10-second
	// timeout and follows no redirects, so the secret is never re-sent to a
	// second address.
	Client *http.Client
	// Now is the gate's clock; nil is the real clock.
	Now func() time.Time
}

type session struct {
	person string
	until  time.Time
}

type gate struct {
	secret []byte
	redeem string
	client *http.Client
	now    func() time.Time
	proxy  *httputil.ReverseProxy

	mu       sync.Mutex
	sessions map[string]session
}

// New returns the gate's handler for cfg.
func New(cfg Config, opts Options) http.Handler {
	g := &gate{
		secret:   []byte(cfg.Secret),
		redeem:   cfg.Forge.JoinPath(RedeemPath).String(),
		client:   opts.Client,
		now:      opts.Now,
		sessions: map[string]session{},
	}
	if g.client == nil {
		g.client = &http.Client{
			Timeout: redeemTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	g.now = clock.Or(g.now)
	upstream := cfg.Upstream
	g.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			// The app's host allow-list is built from its public URL, so it
			// must see the host the person used, not the internal name.
			r.Out.Host = r.In.Host
			// The platform's edge sets these and they are true; Rewrite
			// strips them from the outbound request, so carry them over as
			// they were. SetXForwarded would claim http, because the hop
			// from the edge to the gate is plain http.
			for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Proto"} {
				if v := r.In.Header.Values(h); len(v) > 0 {
					r.Out.Header[h] = append([]string(nil), v...)
				}
			}
			// The secret and the gate's session are the gate's business;
			// the app never sees either.
			r.Out.Header.Del(TokenHeader)
			stripGateCookie(r.Out.Header)
		},
		ModifyResponse: func(res *http.Response) error {
			dropGateSetCookie(res.Header)
			res.Header.Del(RefusedHeader)
			return nil
		},
		// The proxy's error text can name internal addresses; nothing about
		// a request is logged or echoed.
		ErrorLog: log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	return g
}

func (g *gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Entry is answered by the gate whoever asks: the ticket in its query
	// must never reach the app, even on a probe or inside a session.
	if r.URL.Path == EnterPath {
		g.enter(w, r)
		return
	}
	if g.isProbe(r) {
		g.proxy.ServeHTTP(w, r)
		return
	}
	if s, ok := g.sessionFor(r); ok {
		// Everything forwarded under a session, including streams and
		// upgraded connections, ends when the session does. The proxy
		// closes an upgraded connection's backend when this context ends.
		ctx, cancel := context.WithTimeout(r.Context(), s.until.Sub(g.now()))
		defer cancel()
		g.proxy.ServeHTTP(w, r.WithContext(ctx))
		return
	}
	notFound(w)
}

func (g *gate) isProbe(r *http.Request) bool {
	values := r.Header.Values(TokenHeader)
	if len(values) != 1 {
		return false
	}
	// Constant time, so response timing does not reveal how much of a
	// guessed secret was right.
	return subtle.ConstantTimeCompare([]byte(values[0]), g.secret) == 1
}

func (g *gate) sessionFor(r *http.Request) (session, bool) {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range r.Cookies() {
		if c.Name != CookieName {
			continue
		}
		s, ok := g.sessions[c.Value]
		if !ok {
			continue
		}
		if !now.Before(s.until) {
			delete(g.sessions, c.Value)
			continue
		}
		return s, true
	}
	return session{}, false
}

func (g *gate) enter(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		notFound(w)
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")

	s, ok := g.redeemTicket(r.Context(), r.URL.Query().Get("ticket"))
	if !ok {
		h.Set(RefusedHeader, RefusedValue)
		h.Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, enterRefused)
		return
	}
	// The session id is always fresh: the gate never adopts an id offered
	// by the browser or derived from the ticket, so nobody can fix a
	// session in advance and wait for a person to enter on it.
	id := newSessionID()
	g.mu.Lock()
	g.dropExpiredLocked(g.now())
	g.sessions[id] = s
	g.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    id,
		Path:     "/",
		Expires:  s.until,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	h.Set("Location", "/")
	w.WriteHeader(http.StatusSeeOther)
}

// redeemTicket asks the forge to exchange the ticket. Any failure, whatever
// its cause, is a refusal: the forge decides who gets in, and the gate does
// not guess when it cannot hear the answer.
func (g *gate) redeemTicket(ctx context.Context, ticket string) (session, bool) {
	if ticket == "" {
		return session{}, false
	}
	body, err := json.Marshal(struct {
		Ticket string `json:"ticket"`
	}{ticket})
	if err != nil {
		return session{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, redeemTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.redeem, bytes.NewReader(body))
	if err != nil {
		return session{}, false
	}
	req.Header.Set("Authorization", "Bearer "+string(g.secret))
	req.Header.Set("Content-Type", "application/json")
	res, err := g.client.Do(req)
	if err != nil {
		return session{}, false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return session{}, false
	}
	var answer struct {
		Person string `json:"person"`
		Until  string `json:"until"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxRedeemBody)).Decode(&answer); err != nil {
		return session{}, false
	}
	until, err := time.Parse(time.RFC3339, answer.Until)
	if err != nil || answer.Person == "" || !g.now().Before(until) {
		return session{}, false
	}
	return session{person: answer.Person, until: until}, true
}

// dropExpiredLocked forgets sessions past their deadline. It runs on every
// entry, so the map stays bounded by the sessions that are still live.
func (g *gate) dropExpiredLocked(now time.Time) {
	for id, s := range g.sessions {
		if !now.Before(s.until) {
			delete(g.sessions, id)
		}
	}
}

func newSessionID() string {
	b := make([]byte, 32)
	// crypto/rand.Read never returns an error; it crashes the program if
	// the system cannot supply randomness, which is the right outcome here.
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func notFound(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(RefusedHeader, RefusedValue)
	w.WriteHeader(http.StatusNotFound)
}

// stripGateCookie removes the gate's cookie from the Cookie header the app
// receives, leaving the app's own cookies exactly as the browser sent them.
func stripGateCookie(h http.Header) {
	lines := h.Values("Cookie")
	if len(lines) == 0 {
		return
	}
	var kept []string
	for _, line := range lines {
		for _, part := range strings.Split(line, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name, _, _ := strings.Cut(part, "=")
			if strings.TrimSpace(name) == CookieName {
				continue
			}
			kept = append(kept, part)
		}
	}
	if len(kept) == 0 {
		h.Del("Cookie")
		return
	}
	h.Set("Cookie", strings.Join(kept, "; "))
}

// dropGateSetCookie removes any attempt by the app to set the gate's cookie,
// so the app cannot overwrite a person's session or fix one in advance.
func dropGateSetCookie(h http.Header) {
	values := h.Values("Set-Cookie")
	if len(values) == 0 {
		return
	}
	var kept []string
	for _, v := range values {
		name, _, _ := strings.Cut(v, "=")
		if strings.TrimSpace(name) == CookieName {
			continue
		}
		kept = append(kept, v)
	}
	if len(kept) == 0 {
		h.Del("Set-Cookie")
		return
	}
	h["Set-Cookie"] = kept
}
