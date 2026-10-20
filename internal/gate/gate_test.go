package gate

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const testSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // gitleaks:allow (test fixture, not a credential)

// seen is what the fake app recorded about one request it received.
type seen struct {
	path   string
	query  string
	host   string
	header http.Header
}

// fakeApp records every request that reaches it. handler, when set,
// answers instead of the default 200 "app".
type fakeApp struct {
	mu       sync.Mutex
	requests []seen
	handler  http.HandlerFunc
}

func (a *fakeApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	a.requests = append(a.requests, seen{r.URL.Path, r.URL.RawQuery, r.Host, r.Header.Clone()})
	h := a.handler
	a.mu.Unlock()
	if h != nil {
		h(w, r)
		return
	}
	io.WriteString(w, "app")
}

func (a *fakeApp) seen() []seen {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]seen(nil), a.requests...)
}

// fakeForge answers redemptions with answer and records what it was sent.
type fakeForge struct {
	mu       sync.Mutex
	auth     []string
	ctype    []string
	tickets  []string
	status   int
	response string
}

func (f *fakeForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != RedeemPath {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Ticket string `json:"ticket"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.ctype = append(f.ctype, r.Header.Get("Content-Type"))
	f.tickets = append(f.tickets, body.Ticket)
	status, response := f.status, f.response
	f.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	io.WriteString(w, response)
}

func (f *fakeForge) grant(until time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = http.StatusOK
	f.response = `{"person":"ana","until":"` + until.Format(time.RFC3339Nano) + `"}`
}

type harness struct {
	app     *fakeApp
	apptown *fakeForge
	gate    *httptest.Server
	clock   *settable
}

// settable is a settable clock; zero means the real time.
type settable struct {
	mu  sync.Mutex
	now time.Time
}

func (c *settable) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.now.IsZero() {
		return time.Now()
	}
	return c.now
}

func (c *settable) set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{app: &fakeApp{}, apptown: &fakeForge{}, clock: &settable{}}
	appSrv := httptest.NewServer(h.app)
	t.Cleanup(appSrv.Close)
	forgeSrv := httptest.NewServer(h.apptown)
	t.Cleanup(forgeSrv.Close)

	cfg, err := ConfigFromEnv(func(k string) string {
		return map[string]string{
			"GATE_UPSTREAM": appSrv.URL,
			"GATE_SECRET":   testSecret,
			"GATE_APPTOWN":  forgeSrv.URL,
		}[k]
	})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	h.gate = httptest.NewServer(New(cfg, Options{Now: h.clock.Now}))
	t.Cleanup(h.gate.Close)
	return h
}

// noRedirects is a client that shows the gate's own answers.
var noRedirects = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Timeout:       10 * time.Second,
}

func (h *harness) do(t *testing.T, req *http.Request) (*http.Response, string) {
	t.Helper()
	res, err := noRedirects.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, string(body)
}

func (h *harness) get(t *testing.T, path string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.gate.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return h.do(t, req)
}

// enter goes through the person's door and returns the session cookie.
func (h *harness) enter(t *testing.T, until time.Time) *http.Cookie {
	t.Helper()
	h.apptown.grant(until)
	res, _ := h.get(t, EnterPath+"?ticket=tk-1", nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("enter: status %d, want 303", res.StatusCode)
	}
	for _, c := range res.Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	t.Fatal("enter: no session cookie")
	return nil
}

func expectEmpty404(t *testing.T, res *http.Response, body string) {
	t.Helper()
	if res.StatusCode != http.StatusNotFound || body != "" {
		t.Fatalf("got %d %q, want an empty 404", res.StatusCode, body)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control %q, want no-store", got)
	}
	if got := res.Header.Get(RefusedHeader); got != RefusedValue {
		t.Fatalf("%s %q, want %q: AppTown cannot tell this refusal from the app's 404", RefusedHeader, got, RefusedValue)
	}
}

func TestGateRefusesRequestsWithoutACredential(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/", "/health", "/api/anything?x=1"} {
		res, body := h.get(t, path, nil)
		expectEmpty404(t, res, body)
	}
	if n := len(h.app.seen()); n != 0 {
		t.Fatalf("app received %d requests, want none", n)
	}
}

func TestGateRefusesAWrongProbeToken(t *testing.T) {
	h := newHarness(t)
	wrong := []string{
		"",
		strings.Repeat("0", 64),
		testSecret[:63],
		testSecret + "0",
		strings.ToUpper(testSecret),
	}
	for _, token := range wrong {
		req, _ := http.NewRequest(http.MethodGet, h.gate.URL+"/", nil)
		req.Header.Set(TokenHeader, token)
		res, body := h.do(t, req)
		expectEmpty404(t, res, body)
	}
	// Two copies of the header, one of them right, is not a probe either:
	// the gate never picks among candidates.
	req, _ := http.NewRequest(http.MethodGet, h.gate.URL+"/", nil)
	req.Header.Add(TokenHeader, testSecret)
	req.Header.Add(TokenHeader, "nope")
	res, body := h.do(t, req)
	expectEmpty404(t, res, body)
	if n := len(h.app.seen()); n != 0 {
		t.Fatalf("app received %d requests, want none", n)
	}
}

func TestGateForwardsAProbeWithoutItsTokenKeepingHostAndForwardedHeaders(t *testing.T) {
	h := newHarness(t)
	req, _ := http.NewRequest(http.MethodGet, h.gate.URL+"/ready?deep=1", nil)
	req.Host = "copy-abc.ondigitalocean.app"
	req.Header.Set(TokenHeader, testSecret)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	res, body := h.do(t, req)
	if res.StatusCode != http.StatusOK || body != "app" {
		t.Fatalf("got %d %q, want the app's answer", res.StatusCode, body)
	}
	got := h.app.seen()
	if len(got) != 1 {
		t.Fatalf("app received %d requests, want 1", len(got))
	}
	r := got[0]
	if r.path != "/ready" || r.query != "deep=1" {
		t.Fatalf("app saw %s?%s", r.path, r.query)
	}
	if v := r.header.Values(TokenHeader); len(v) != 0 {
		t.Fatal("the probe token reached the app")
	}
	if r.host != "copy-abc.ondigitalocean.app" {
		t.Fatalf("app saw Host %q, want the public host", r.host)
	}
	if v := r.header.Get("X-Forwarded-Proto"); v != "https" {
		t.Fatalf("app saw X-Forwarded-Proto %q, want https", v)
	}
	if v := r.header.Get("X-Forwarded-For"); v != "203.0.113.7" {
		t.Fatalf("app saw X-Forwarded-For %q, want the platform's value", v)
	}
}

func TestEnteringWithATicketRedeemsItAndStartsASession(t *testing.T) {
	h := newHarness(t)
	until := time.Now().Add(time.Hour).Truncate(time.Second)
	h.apptown.grant(until)
	res, _ := h.get(t, EnterPath+"?ticket=tk-secret-ticket", nil)

	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		t.Fatalf("got %d to %q, want 303 to /", res.StatusCode, res.Header.Get("Location"))
	}
	if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("headers %v, want no-store and no-referrer", res.Header)
	}
	var setCookie string
	for _, v := range res.Header.Values("Set-Cookie") {
		if strings.HasPrefix(v, CookieName+"=") {
			setCookie = v
		}
	}
	for _, attr := range []string{"Path=/", "Secure", "HttpOnly", "SameSite=Lax", "Expires="} {
		if !strings.Contains(setCookie, attr) {
			t.Fatalf("Set-Cookie %q lacks %s", setCookie, attr)
		}
	}
	if strings.Contains(setCookie, "Domain=") || strings.Contains(setCookie, "tk-secret-ticket") {
		t.Fatalf("Set-Cookie %q must have no Domain and no trace of the ticket", setCookie)
	}
	c := res.Cookies()[0]
	if len(c.Value) != 64 || !c.Expires.Equal(until) {
		t.Fatalf("cookie value length %d expires %v, want 64 hex chars expiring %v", len(c.Value), c.Expires, until)
	}

	h.apptown.mu.Lock()
	auth, ctype, tickets := h.apptown.auth, h.apptown.ctype, h.apptown.tickets
	h.apptown.mu.Unlock()
	if len(tickets) != 1 || tickets[0] != "tk-secret-ticket" {
		t.Fatalf("AppTown received tickets %v", tickets)
	}
	if auth[0] != "Bearer "+testSecret || ctype[0] != "application/json" {
		t.Fatal("redemption did not carry the gate secret as a bearer token with a JSON body")
	}
	if n := len(h.app.seen()); n != 0 {
		t.Fatalf("app received %d requests during entry, want none", n)
	}

	// The cookie now opens the app.
	res, body := h.get(t, "/", map[string]string{"Cookie": CookieName + "=" + c.Value})
	if res.StatusCode != http.StatusOK || body != "app" {
		t.Fatalf("with session: %d %q", res.StatusCode, body)
	}
}

func TestEntryIsNeverForwardedEvenWithAValidCredential(t *testing.T) {
	h := newHarness(t)
	cookie := h.enter(t, time.Now().Add(time.Hour))
	h.apptown.mu.Lock()
	h.apptown.status, h.apptown.response = http.StatusGone, ""
	h.apptown.mu.Unlock()

	res, _ := h.get(t, EnterPath+"?ticket=tk-2", map[string]string{
		TokenHeader: testSecret,
		"Cookie":    CookieName + "=" + cookie.Value,
	})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("got %d, want the gate's own 403", res.StatusCode)
	}
	res, body := h.get(t, EnterPath+"?ticket=tk-3", nil)
	expectRefusedEntry(t, res, body)
	// Other methods on the entry path get the plain 404.
	req, _ := http.NewRequest(http.MethodPost, h.gate.URL+EnterPath+"?ticket=tk-4", nil)
	req.Header.Set(TokenHeader, testSecret)
	res, body = h.do(t, req)
	expectEmpty404(t, res, body)

	for _, r := range h.app.seen() {
		if r.path == EnterPath || strings.Contains(r.query, "tk-") {
			t.Fatalf("app saw the entry request %s?%s", r.path, r.query)
		}
	}
}

func TestFailedRedemptionIsRefusedWithoutASession(t *testing.T) {
	cases := map[string]func(f *fakeForge){
		// A refusal is a refusal even if its body looks like a grant.
		"AppTown refuses": func(f *fakeForge) {
			f.grant(time.Now().Add(time.Hour))
			f.status = http.StatusForbidden
		},
		"AppTown errors": func(f *fakeForge) { f.status, f.response = http.StatusInternalServerError, "" },
		"bad json":       func(f *fakeForge) { f.status, f.response = http.StatusOK, `{"person":` },
		"until passed": func(f *fakeForge) {
			f.status = http.StatusOK
			f.response = `{"person":"ana","until":"` + time.Now().Add(-time.Minute).Format(time.RFC3339) + `"}`
		},
		"no person": func(f *fakeForge) {
			f.status = http.StatusOK
			f.response = `{"until":"` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"}`
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			setup(h.apptown)
			res, body := h.get(t, EnterPath+"?ticket=tk-1", nil)
			expectRefusedEntry(t, res, body)
		})
	}
	t.Run("AppTown unreachable", func(t *testing.T) {
		h := newHarness(t)
		dead := httptest.NewServer(http.NotFoundHandler())
		deadURL, _ := url.Parse(dead.URL)
		dead.Close()
		cfg := Config{Upstream: mustURL(t, "http://127.0.0.1:1"), Secret: testSecret, Forge: deadURL}
		srv := httptest.NewServer(New(cfg, Options{}))
		t.Cleanup(srv.Close)
		h.gate = srv
		res, body := h.get(t, EnterPath+"?ticket=tk-1", nil)
		expectRefusedEntry(t, res, body)
	})
	t.Run("no ticket", func(t *testing.T) {
		h := newHarness(t)
		h.apptown.grant(time.Now().Add(time.Hour))
		res, body := h.get(t, EnterPath, nil)
		expectRefusedEntry(t, res, body)
		if len(h.apptown.tickets) != 0 {
			t.Fatal("an empty ticket was sent to AppTown")
		}
	})
}

func expectRefusedEntry(t *testing.T, res *http.Response, body string) {
	t.Helper()
	if res.StatusCode != http.StatusForbidden || body != enterRefused {
		t.Fatalf("got %d %q, want 403 with the short refusal", res.StatusCode, body)
	}
	if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Referrer-Policy") != "no-referrer" || res.Header.Get(RefusedHeader) != RefusedValue {
		t.Fatalf("headers %v, want no-store, no-referrer and the gate's refusal mark", res.Header)
	}
	if len(res.Header.Values("Set-Cookie")) != 0 {
		t.Fatal("a refused entry set a cookie")
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestSessionIsForwardedWithoutTheGateCookie(t *testing.T) {
	h := newHarness(t)
	c := h.enter(t, time.Now().Add(time.Hour))
	res, body := h.get(t, "/x", map[string]string{
		"Cookie": "app_session=s1; " + CookieName + "=" + c.Value + "; theme=dark",
	})
	if res.StatusCode != http.StatusOK || body != "app" {
		t.Fatalf("got %d %q", res.StatusCode, body)
	}
	got := h.app.seen()
	if v := got[len(got)-1].header.Values("Cookie"); len(v) != 1 || v[0] != "app_session=s1; theme=dark" {
		t.Fatalf("app saw Cookie %q, want the app's cookies only", v)
	}

	// With only the gate cookie, the app sees no Cookie header at all.
	h.get(t, "/y", map[string]string{"Cookie": CookieName + "=" + c.Value})
	got = h.app.seen()
	if v := got[len(got)-1].header.Values("Cookie"); len(v) != 0 {
		t.Fatalf("app saw Cookie %q, want none", v)
	}
}

func TestUnknownOrExpiredSessionIsRefused(t *testing.T) {
	h := newHarness(t)
	start := time.Now()
	h.clock.set(start)
	c := h.enter(t, start.Add(time.Hour))

	res, body := h.get(t, "/", map[string]string{"Cookie": CookieName + "=" + strings.Repeat("a", 64)})
	expectEmpty404(t, res, body)

	h.clock.set(start.Add(time.Hour))
	res, body = h.get(t, "/", map[string]string{"Cookie": CookieName + "=" + c.Value})
	expectEmpty404(t, res, body)
	if n := len(h.app.seen()); n != 0 {
		t.Fatalf("app received %d requests, want none", n)
	}
}

func TestAppCannotSetTheGateCookie(t *testing.T) {
	h := newHarness(t)
	h.app.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", CookieName+"=planted; Path=/; Secure")
		w.Header().Add("Set-Cookie", "app_session=s2; Path=/; HttpOnly")
		w.Header().Add("Set-Cookie", " "+CookieName+"=planted2")
		w.Header().Add("Set-Cookie", "__host-forge-gate-other=fine")
	}
	res, _ := h.get(t, "/", map[string]string{TokenHeader: testSecret})
	got := res.Header.Values("Set-Cookie")
	want := []string{"app_session=s2; Path=/; HttpOnly", "__host-forge-gate-other=fine"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Set-Cookie %q, want %q", got, want)
	}
}

// The app cannot answer as if the gate had refused: the gate's refusal
// mark is removed from everything the app sends, so a 404 forwarded from
// the app never reads as the gate's own.
func TestAppCannotMarkItsAnswerAsTheGatesRefusal(t *testing.T) {
	h := newHarness(t)
	h.app.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(RefusedHeader, RefusedValue)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNotFound)
	}
	res, _ := h.get(t, "/", map[string]string{TokenHeader: testSecret})
	if res.StatusCode != http.StatusNotFound || res.Header.Get(RefusedHeader) != "" {
		t.Fatalf("the app's 404 reads %d with %s %q", res.StatusCode, RefusedHeader, res.Header.Get(RefusedHeader))
	}
}

func TestUpstreamFailureIsA502WithoutDetail(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	cfg := Config{Upstream: mustURL(t, deadURL), Secret: testSecret, Forge: mustURL(t, "http://127.0.0.1:1")}
	srv := httptest.NewServer(New(cfg, Options{}))
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set(TokenHeader, testSecret)
	res, err := noRedirects.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusBadGateway || len(body) != 0 {
		t.Fatalf("got %d %q, want an empty 502", res.StatusCode, body)
	}
}

// sessionDeadline is short so the stream tests finish quickly; the margin
// allows for a slow CI machine without hiding a stream that never ends.
const (
	sessionDeadline = 200 * time.Millisecond
	closeMargin     = 5 * time.Second
)

func TestStreamingResponseEndsAtTheSessionDeadline(t *testing.T) {
	h := newHarness(t)
	h.app.handler = func(w http.ResponseWriter, r *http.Request) {
		// The app streams for as long as anyone listens; it never ends the
		// response itself.
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
				if _, err := io.WriteString(w, "tick\n"); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
		}
	}
	until := time.Now().Add(sessionDeadline)
	c := h.enter(t, until)

	req, _ := http.NewRequest(http.MethodGet, h.gate.URL+"/events", nil)
	req.Header.Set("Cookie", CookieName+"="+c.Value)
	client := &http.Client{Timeout: closeMargin + time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	br := bufio.NewReader(res.Body)
	if line, err := br.ReadString('\n'); err != nil || line != "tick\n" {
		t.Fatalf("stream did not start: %q %v", line, err)
	}
	_, err = io.Copy(io.Discard, br)
	ended := time.Now()
	if ended.Before(until) {
		t.Fatalf("stream ended %v before the deadline", until.Sub(ended))
	}
	if ended.After(until.Add(closeMargin)) {
		t.Fatalf("stream ran %v past the deadline", ended.Sub(until))
	}
	_ = err // a cut stream may read as EOF or an unexpected EOF; both are an end
}

func TestUpgradedConnectionClosesAtTheSessionDeadline(t *testing.T) {
	h := newHarness(t)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	h.app.handler = func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "want upgrade", http.StatusBadRequest)
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		rw.Flush()
		// The app keeps the connection open and talking; only the gate can
		// end it.
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			if _, err := conn.Write([]byte("m")); err != nil {
				return
			}
		}
	}
	until := time.Now().Add(sessionDeadline)
	c := h.enter(t, until)

	conn, err := net.Dial("tcp", strings.TrimPrefix(h.gate.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(closeMargin + time.Second))
	io.WriteString(conn, "GET /ws HTTP/1.1\r\nHost: copy.example\r\n"+
		"Connection: Upgrade\r\nUpgrade: websocket\r\n"+
		"Cookie: "+CookieName+"="+c.Value+"\r\n\r\n")
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("got %d, want 101", res.StatusCode)
	}
	if b, err := br.ReadByte(); err != nil || b != 'm' {
		t.Fatalf("upgraded connection carried no data: %v", err)
	}
	_, err = io.Copy(io.Discard, br)
	ended := time.Now()
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("upgraded connection still open %v after the deadline", ended.Sub(until))
	}
	if ended.Before(until) {
		t.Fatalf("connection ended %v before the deadline", until.Sub(ended))
	}
	if ended.After(until.Add(closeMargin)) {
		t.Fatalf("connection ran %v past the deadline", ended.Sub(until))
	}
}
