package apptown

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/deploy/docker"
)

func TestBrowserMutationOrigins(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{PublicHost: "forge.example.test", TierDomains: []string{"preview=preview.example.test"}})

	h := a.browserSecurity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		name, origin, site string
		want               int
	}{
		{"native client", "", "", 204},
		{"browser", "https://forge.example.test", "same-origin", 204},
		{"old browser", "https://forge.example.test", "", 204},
		{"sibling preview", "https://evil.preview.example.test", "same-site", 403},
		{"unrelated site", "https://evil.other.test", "cross-site", 403},
		{"opaque origin", "null", "", 403},
		{"other port", "https://forge.example.test:444", "", 403},
		{"downgrade", "http://forge.example.test", "", 403},
		{"metadata alone", "", "same-site", 403},
		{"contradictory metadata", "https://evil.other.test", "same-origin", 403},
		{"origin with path", "https://forge.example.test/", "", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
				r := httptest.NewRequest(method, "http://forge.example.test/api/v1/projects", nil)
				if tc.origin != "" {
					r.Header.Set("Origin", tc.origin)
				}
				r.Header.Set("Sec-Fetch-Site", tc.site)
				// Neither bearer-looking input nor proxy headers bypass the
				// browser boundary or select an attacker-supplied origin.
				r.Header.Set("Authorization", "Bearer apptownd")
				r.Header.Set("X-Forwarded-Host", "evil.other.test")
				r.Header.Set("X-Forwarded-Proto", "http")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != tc.want {
					t.Fatalf("%s: got %d, want %d", method, w.Code, tc.want)
				}
			}
		})
	}
	// A duplicated Origin cannot be interpreted differently by different layers.
	r := httptest.NewRequest("POST", "http://forge.example.test/api/v1/projects", nil)
	r.Header.Add("Origin", "https://forge.example.test")
	r.Header.Add("Origin", "https://evil.other.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("duplicate origin accepted: %d", w.Code)
	}
}

func TestBrowserForgeryDoesNotMutate(t *testing.T) {
	t.Parallel()
	a, h, who := oauthFixture(t)
	for _, path := range []string{"/api/v1/auth/logout", "/api/v1/projects"} {
		r := httptest.NewRequest("POST", path, strings.NewReader(`{"slug":"apptownd","name":"apptownd"}`))
		r.AddCookie(who)
		r.Header.Set("Origin", "https://evil.example.test")
		r.Header.Set("Sec-Fetch-Site", "same-site")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("apptownd %s: %d", path, w.Code)
		}
	}
	if p, err := a.Projects.BySlug(context.Background(), "apptownd"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("forgery created project: %+v %v", p, err)
	}
	r := httptest.NewRequest("GET", "/api/v1/me", nil)
	r.AddCookie(who)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("forgery ended session: %d", w.Code)
	}
}

func TestTerminalRejectsOriginBeforeAttachment(t *testing.T) {
	t.Parallel()
	// Only missing-session lookup is exercised; no agent can be attached.
	a := &App{
		cfg:     testConfig(t, Config{PublicHost: "forge.example.test", TierDomains: []string{"preview=preview.example.test"}}),
		Console: console.New(console.Dependencies{}),
	}

	for _, origin := range []string{"", "null", "https://evil.preview.example.test", "https://evil.other.test", "http://forge.example.test", "https://forge.example.test:444"} {
		r := httptest.NewRequest("GET", "http://forge.example.test/api/v1/console/missing/pty", nil)
		r.SetPathValue("sid", "missing")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		a.handleConsolePTY(w, r)
		if w.Code != 403 {
			t.Fatalf("origin %q reached console lookup/attachment: %d", origin, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "http://forge.example.test/api/v1/console/missing/pty", nil)
	r.Header.Set("Origin", "https://forge.example.test")
	r.SetPathValue("sid", "missing")
	w := httptest.NewRecorder()
	a.handleConsolePTY(w, r)
	if w.Code != 404 {
		t.Fatalf("same-origin request should reach console lookup: %d", w.Code)
	}
}

func TestProductionBrowserCookie(t *testing.T) {
	t.Parallel()
	a, _ := internalApp(t, Config{PublicHost: "forge.example.test", TierDomains: []string{"preview=preview.example.test"}})
	h := a.Handler()
	w := postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "boss", "boss-password"))
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatalf("bootstrap: %d %s", w.Code, w.Body.String())
	}
	c := w.Result().Cookies()[0]
	if c.Name != secureSessionCookie || !c.Secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unsafe production cookie: %+v", c)
	}
	for _, name := range []string{sessionCookie, secureSessionCookie} {
		r := httptest.NewRequest("GET", "/api/v1/me", nil)
		r.AddCookie(&http.Cookie{Name: name, Value: c.Value})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 200
		if name == sessionCookie {
			want = 401 // never accept a sibling's legacy Domain cookie
		}
		if w.Code != want {
			t.Fatalf("%s: %d, want %d", name, w.Code, want)
		}
	}
	w = postJSON(h, "/api/v1/auth/logout", nil, c)
	cleared := w.Result().Cookies()
	if w.Code != 200 || len(cleared) != 1 || cleared[0].Name != secureSessionCookie || !cleared[0].Secure || cleared[0].MaxAge != -1 {
		t.Fatalf("logout must clear secure cookie: %d %v", w.Code, cleared)
	}
	r := httptest.NewRequest("GET", "/api/v1/me", nil)
	r.AddCookie(c)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("logged-out session still works: %d", w.Code)
	}
}

func TestBrowserDevelopmentOrigin(t *testing.T) {
	t.Parallel()
	a := &App{}
	r := httptest.NewRequest("POST", "http://localhost:5173/api/v1/auth/login", nil)
	r.Header.Set("Origin", "http://localhost:5173")
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "evil.example.test")
	if !a.sameBrowserOrigin(r) || a.secureBrowser(r) || a.sessionCookieName(r) != sessionCookie {
		t.Fatal("development proxy should preserve Host; arbitrary forwarded headers must not choose cookie/origin policy")
	}
	r = httptest.NewRequest("POST", "https://localhost:7777/api/v1/auth/login", nil)
	if !a.secureBrowser(r) || a.sessionCookieName(r) != secureSessionCookie {
		t.Fatal("direct TLS also needs a secure cookie")
	}
}

const browserTestPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a17sAAAAASUVORK5CYII="

func TestUntrustedFileResponses(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")

	changeID := att_seedChange(t, a)
	png, err := base64.StdEncoding.DecodeString(browserTestPNG)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, typ, disposition string
		body                   []byte
	}{
		{"attack.html", "application/octet-stream", "attachment", []byte(`<script>fetch('/api/v1/projects')</script>`)},
		{"attack.svg", "application/octet-stream", "attachment", []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`)},
		{"pretend.png", "application/octet-stream", "attachment", []byte(`<!doctype html><script>alert(1)</script>`)},
		{"report.pdf", "application/octet-stream", "attachment", []byte("%PDF-1.7")},
		{"quote \" ü.csv", "application/octet-stream", "attachment", []byte("a,b\n1,2")},
		{"picture.png", "image/png", "inline", png},
		{"picture.html", "image/png", "inline", png},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := att_upload(a, changeID, tc.name, tc.body)
			if w.Code != 201 {
				t.Fatalf("upload: %d %s", w.Code, w.Body.String())
			}
			path := fmt.Sprintf("/api/v1/changes/%d/attachments/%s", changeID, url.PathEscape(tc.name))
			for _, method := range []string{"GET", "HEAD"} {
				w = att_json(a, method, path)
				d, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
				if w.Code != 200 || w.Header().Get("Content-Type") != tc.typ || d != tc.disposition || err != nil || params["filename"] != tc.name {
					t.Fatalf("unsafe %s headers: %d %v %v", method, w.Code, w.Header(), err)
				}
				if method == "GET" && !bytes.Equal(w.Body.Bytes(), tc.body) {
					t.Fatal("download bytes changed")
				}
				if w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox;") {
					t.Fatalf("missing content defenses: %v", w.Header())
				}
			}
		})
	}
	// The other raw-file door must use the same content-based policy even
	// though console filenames themselves only allow picture extensions.
	dir := filepath.Join(a.cfg.dataDir, "console", "files", "uploads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "picture-1.png"), []byte("<script>alert(1)</script>"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.Console.Restore(withLabel(consoleMetaFor("files", idstest.N(1), ids.Nil), conversation.SlugLabel, "p"))
	w := att_json(a, "GET", "/api/v1/console/files/uploads/picture-1.png")
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("console bypasses file policy: %d %v", w.Code, w.Header())
	}
}

func TestAppTownPagesCannotBeFramed(t *testing.T) {
	t.Parallel()
	_, h := internalApp(t)
	for _, path := range []string{"/", "/settings", "/oauth/authorize", "/api/v1/auth/me"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatalf("%s is frameable: %v", path, w.Header())
		}
	}
}

func TestBrowserPolicyLeavesHostedAppPolicy(t *testing.T) {
	t.Parallel()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "frame-ancestors https://embed.example.test")
		w.WriteHeader(201)
	}))
	defer app.Close()
	u, err := url.Parse(app.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	a := testApp(t, Config{AuthMode: "internal", AuthHeader: "", AuthProxySecret: "", TierDomains: []string{"preview=preview.example.test"}, PublicHost: "forge.example.test"})

	label := fmt.Sprintf("change-%d", createReadTestChange(t, a, createReadTestProject(t, a, "hosted"), "hosted"))
	a.vhostCache = map[string]vhostEntry{docker.PreviewLabelName(label): {port: port, at: time.Now()}}
	r := httptest.NewRequest("POST", "http://"+label+".preview.example.test/submit", nil)
	r.Header.Set("Origin", "https://embed.example.test")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != 201 || w.Header().Get("X-Frame-Options") != "" || w.Header().Get("Content-Security-Policy") != "frame-ancestors https://embed.example.test" {
		t.Fatalf("AppTown policy leaked into hosted app: %d %v", w.Code, w.Header())
	}
}

func TestHostedAppProxyPreservesPublicOriginAndRequest(t *testing.T) {
	t.Parallel()
	observed := make(chan *http.Request, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- r.Clone(r.Context())
		w.WriteHeader(http.StatusCreated)
	}))
	defer backend.Close()
	u, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	a := testApp(t, Config{TierDomains: []string{"preview=https://preview.example.test"}})

	label := fmt.Sprintf("change-%d", createReadTestChange(t, a, createReadTestProject(t, a, "hosted"), "hosted"))
	a.vhostCache = map[string]vhostEntry{docker.PreviewLabelName(label): {port: port, at: time.Now()}}
	r := httptest.NewRequest("POST", "http://"+label+".preview.example.test/a%20b?version=old&x=1", nil)
	r.RemoteAddr = "192.0.2.4:1234"
	r.Header.Set("X-Forwarded-Host", "attacker.example")
	r.Header.Set("X-Forwarded-Proto", "http")
	r.Header.Set("X-Forwarded-For", "spoofed")
	r.Header.Set("Connection", "X-Forwarded-Proto")
	w := httptest.NewRecorder()
	a.vHost(http.NotFoundHandler()).ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("proxy: %d %s", w.Code, w.Body.String())
	}
	got := <-observed
	if got.Host != r.Host || got.RequestURI != "/a%20b?version=old&x=1" || got.Method != "POST" {
		t.Fatalf("changed app request: %s %s %s", got.Host, got.Method, got.RequestURI)
	}
	if got.Header.Get("X-Forwarded-Host") != r.Host || got.Header.Get("X-Forwarded-Proto") != "https" || got.Header.Get("X-Forwarded-For") != "192.0.2.4" {
		t.Fatalf("incorrect public origin: %v", got.Header)
	}
}

// An invite or password reset link carries its token in the path, so the
// page it opens sends no referrer; other pages keep same-origin.
func TestInviteLinkPagesSendNoReferrer(t *testing.T) {
	t.Parallel()
	_, h := internalApp(t)
	for path, want := range map[string]string{"/invite/apptown_invite_abc": "no-referrer", "/people": "same-origin"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if got := w.Header().Get("Referrer-Policy"); got != want {
			t.Fatalf("%s: Referrer-Policy %q, want %q", path, got, want)
		}
	}
}

func TestBrowserResourcePolicy(t *testing.T) {
	t.Parallel()
	for _, origin := range []string{"http://forge.test:7777", "https://forge.test:8443"} {
		a := testApp(t, Config{PublicHost: origin})
		r := httptest.NewRequest("GET", "http://untrusted.test/", nil)
		r.Header.Set("X-Forwarded-Host", "attacker.test")
		r.Header.Set("X-Forwarded-Proto", "https")
		policy := browserResourcePolicy(a.browserOrigin(r), "")
		for _, directive := range []string{"default-src 'none'", "script-src 'self'", "font-src 'self'", "style-src 'self' 'unsafe-inline'", "img-src 'self' data: https://apptown.dev", "connect-src 'self' " + strings.Replace(origin, "http", "ws", 1)} {
			if !strings.Contains(policy, directive+";") {
				t.Fatalf("missing %q: %s", directive, policy)
			}
		}
		if strings.Contains(policy, "untrusted") || strings.Contains(policy, "attacker") {
			t.Fatal(policy)
		}
	}
}
