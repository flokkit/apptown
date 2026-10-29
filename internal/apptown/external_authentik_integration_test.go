//go:build integration

package apptown

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/execfile"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/testinputs"
)

const authentikProofImage = "ghcr.io/goauthentik/server:2026.8.3@sha256:ab9b4e8cc4ab3f8d1198d2db6aeea66bafea1963b3f2843589e0d163f97d9849"
const authentikProofPostgres = "postgres:16.15-alpine@sha256:f4610795587492cd4fb887c2dc3de301b813278bb6f616992061141b5f15ac36"

type authentikProof struct {
	origin, token, password, issuer, clientSecret string
	client                                        *http.Client
}

// The maintained runner's immutable job identity lets its outer queue finalizer
// settle this exact project if interruption prevents t.Cleanup from executing.
func authentikProofProject(tempRoot string) (string, error) {
	base := filepath.Base(tempRoot)
	if job, ok := strings.CutPrefix(base, "apptown-ci-"); ok {
		if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(job) {
			return "", fmt.Errorf("invalid Authentik proof runner job")
		}
		return "apptown-oidc-proof-" + job, nil
	}
	return "apptown-oidc-proof-" + ids.New(time.Now()).String(), nil
}

// The provider has no host mounts or Docker socket. Everything it can mutate is
// inside this exact randomly named Compose project, including its database.
func startAuthentikProof(t *testing.T) *authentikProof {
	t.Helper()
	project, err := authentikProofProject(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.json")
	p := &authentikProof{token: ids.New(time.Now()).String(), password: ids.New(time.Now()).String(), clientSecret: ids.New(time.Now()).String(), client: &http.Client{Timeout: 10 * time.Second}}
	env := map[string]string{"AUTHENTIK_SECRET_KEY": ids.New(time.Now()).String(), "AUTHENTIK_POSTGRESQL__HOST": "postgres", "AUTHENTIK_POSTGRESQL__USER": "authentik", "AUTHENTIK_POSTGRESQL__NAME": "authentik", "AUTHENTIK_POSTGRESQL__PASSWORD": p.password, "AUTHENTIK_BOOTSTRAP_PASSWORD": ids.New(time.Now()).String(), "AUTHENTIK_BOOTSTRAP_TOKEN": p.token, "AUTHENTIK_BOOTSTRAP_EMAIL": "proof@example.invalid", "AUTHENTIK_ERROR_REPORTING__ENABLED": "false"}
	services := map[string]any{
		"postgres": map[string]any{"image": authentikProofPostgres, "environment": map[string]string{"POSTGRES_USER": "authentik", "POSTGRES_DB": "authentik", "POSTGRES_PASSWORD": p.password}, "volumes": []string{"database:/var/lib/postgresql/data"}, "healthcheck": map[string]any{"test": []string{"CMD-SHELL", "pg_isready -U authentik"}, "interval": "2s", "timeout": "2s", "retries": 30}},
		"server":   map[string]any{"image": authentikProofImage, "command": "server", "environment": env, "depends_on": map[string]any{"postgres": map[string]string{"condition": "service_healthy"}}, "ports": []string{"127.0.0.1::9000"}},
		"worker":   map[string]any{"image": authentikProofImage, "command": "worker", "environment": env, "depends_on": map[string]any{"postgres": map[string]string{"condition": "service_healthy"}}},
	}
	raw, e := json.Marshal(map[string]any{"services": services, "volumes": map[string]any{"database": map[string]any{}}})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(file, raw, 0600); e != nil {
		t.Fatal(e)
	}
	run := func(ctx context.Context, args ...string) execution.Outcome {
		return (execution.Runner{}).Run(ctx, execution.Local, execution.Request{Argv: append([]string{"docker"}, args...), Timeout: 5 * time.Minute})
	}
	compose := func(ctx context.Context, args ...string) execution.Outcome {
		return run(ctx, append([]string{"compose", "--project-name", project, "--file", file}, args...)...)
	}
	// A reused job must not turn a previous provider database into fresh proof.
	for _, kind := range []string{"container", "volume", "network"} {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		args := []string{kind, "ls", "--quiet", "--filter", "label=com.docker.compose.project=" + project}
		if kind == "container" {
			args = append(args, "--all")
		}
		out := run(ctx, args...)
		cancel()
		if !out.Exited0() || strings.TrimSpace(string(out.Output)) != "" {
			t.Fatalf("Authentik proof %s has existing or unknown %s inventory", project, kind)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out := compose(ctx, "down", "--volumes", "--remove-orphans", "--timeout", "15")
		if !out.Exited0() {
			t.Errorf("owned Authentik teardown %s did not complete (exit %d)", project, out.ExitCode)
		}
		for _, kind := range []string{"container", "volume", "network"} {
			args := []string{kind, "ls", "--quiet", "--filter", "label=com.docker.compose.project=" + project}
			if kind == "container" {
				args = append(args, "--all")
			}
			out = run(ctx, args...)
			if !out.Exited0() || strings.TrimSpace(string(out.Output)) != "" {
				t.Errorf("owned Authentik %s %s absence not established; observed IDs: %s", project, kind, strings.TrimSpace(string(out.Output)))
			}
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	if out := compose(ctx, "up", "--detach"); !out.Exited0() {
		t.Fatalf("disposable Authentik startup failed (exit %d; output withheld to protect fixture credentials)", out.ExitCode)
	}
	port := compose(ctx, "port", "server", "9000")
	if !port.Exited0() {
		t.Fatal("provider port observation failed")
	}
	address := strings.TrimSpace(string(port.Output))
	if !strings.HasPrefix(address, "127.0.0.1:") {
		t.Fatal("provider port is not loopback")
	}
	p.origin = "http://" + address
	// Inspect exact created containers rather than reporting a configured pin as
	// observed running identity. Docker resolves the immutable index per platform.
	for _, service := range []string{"server", "worker"} {
		out := compose(ctx, "ps", "--quiet", service)
		if !out.Exited0() {
			t.Fatal("provider container identity unavailable")
		}
		id := strings.TrimSpace(string(out.Output))
		if id == "" || strings.ContainsAny(id, "\r\n ") {
			t.Fatal("provider container identity ambiguous")
		}
		out = run(ctx, "inspect", "--format", "{{.Config.Image}} {{.Image}}", id)
		if !out.Exited0() || !strings.HasPrefix(string(out.Output), authentikProofImage+" sha256:") {
			t.Fatal("provider running image does not match pin")
		}
		t.Logf("%s image: %s", service, strings.TrimSpace(string(out.Output)))
	}
	ready, cancelReady := context.WithTimeout(ctx, 5*time.Minute)
	defer cancelReady()
	for {
		_, status, err := p.api(ready, "GET", "/api/v3/core/users/?username=akadmin", nil)
		if err == nil && status == 200 {
			break
		}
		if poll.Wait(ready, time.Second) != nil {
			t.Fatal("disposable Authentik bootstrap did not become ready")
		}
	}
	return p
}

func (p *authentikProof) api(ctx context.Context, method, path string, body any) (map[string]any, int, error) {
	raw, e := json.Marshal(body)
	if e != nil {
		return nil, 0, e
	}
	req, e := http.NewRequestWithContext(ctx, method, p.origin+path, bytes.NewReader(raw))
	if e != nil {
		return nil, 0, e
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")
	resp, e := p.client.Do(req)
	if e != nil {
		return nil, 0, e
	}
	defer resp.Body.Close()
	var result map[string]any
	if resp.StatusCode != 204 {
		e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result)
	}
	return result, resp.StatusCode, e
}
func (p *authentikProof) mustAPI(t *testing.T, method, path string, body any) map[string]any {
	t.Helper()
	out, status, e := p.api(t.Context(), method, path, body)
	if e != nil || status < 200 || status >= 300 {
		t.Fatalf("provider fixture API %s %s failed status %d (body withheld)", method, path, status)
	}
	return out
}
func (p *authentikProof) first(t *testing.T, path string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	row, err := p.firstRecord(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func (p *authentikProof) firstRecord(ctx context.Context, path string) (map[string]any, error) {
	// Bootstrap credentials can exist before the worker applies default blueprints.
	for {
		out, status, e := p.api(ctx, "GET", path, nil)
		if status == 200 && e != nil {
			return nil, fmt.Errorf("invalid provider fixture inventory: %s", path)
		}
		if e == nil && status == 200 {
			rows, ok := out["results"].([]any)
			if !ok {
				return nil, fmt.Errorf("invalid provider fixture inventory: %s", path)
			}
			if len(rows) > 1 {
				return nil, fmt.Errorf("provider fixture inventory ambiguous: %s", path)
			}
			if len(rows) == 1 {
				row, ok := rows[0].(map[string]any)
				if !ok {
					return nil, fmt.Errorf("invalid provider fixture record: %s", path)
				}
				return row, nil
			}
		}
		if err := poll.Wait(ctx, time.Second); err != nil {
			return nil, fmt.Errorf("provider fixture inventory not ready: %s: %w", path, err)
		}
	}
}

func (p *authentikProof) scopeMappings(ctx context.Context) ([]any, error) {
	var mappings []any
	for _, scope := range []string{"openid", "profile", "email"} {
		row, err := p.firstRecord(ctx, "/api/v3/propertymappings/provider/scope/?scope_name="+scope)
		if err != nil {
			return nil, err
		}
		pk, ok := row["pk"].(string)
		if row["scope_name"] != scope || !ok || pk == "" {
			return nil, fmt.Errorf("invalid provider fixture scope record: %s", scope)
		}
		mappings = append(mappings, pk)
	}
	return mappings, nil
}

func (p *authentikProof) configure(t *testing.T, callback string) {
	t.Helper()
	authentication := p.first(t, "/api/v3/flows/instances/?slug=default-authentication-flow")
	authFlow := p.first(t, "/api/v3/flows/instances/?slug=default-provider-authorization-implicit-consent")
	invalidation := p.first(t, "/api/v3/flows/instances/?slug=default-provider-invalidation-flow")
	key := p.first(t, "/api/v3/crypto/certificatekeypairs/?name=authentik%20Self-signed%20Certificate")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	mappings, err := p.scopeMappings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	user := p.mustAPI(t, "POST", "/api/v3/core/users/", map[string]any{"username": "proof-person", "name": "Proof person", "is_active": true, "type": "internal", "email": "proof-person@example.invalid", "groups": []string{}})
	p.mustAPI(t, "POST", fmt.Sprintf("/api/v3/core/users/%.0f/set_password/", user["pk"]), map[string]string{"password": p.password})
	provider := p.mustAPI(t, "POST", "/api/v3/providers/oauth2/", map[string]any{"name": "AppTown proof", "authentication_flow": authentication["pk"], "authorization_flow": authFlow["pk"], "invalidation_flow": invalidation["pk"], "client_type": "confidential", "client_id": "apptown-proof", "client_secret": p.clientSecret, "signing_key": key["pk"], "redirect_uris": []map[string]string{{"matching_mode": "strict", "url": callback}}, "property_mappings": mappings, "sub_mode": "hashed_user_id", "issuer_mode": "per_provider", "include_claims_in_id_token": true, "grant_types": []string{"authorization_code"}})
	p.mustAPI(t, "POST", "/api/v3/core/applications/", map[string]any{"name": "AppTown proof", "slug": "apptown-proof", "provider": provider["pk"], "policy_engine_mode": "all"})
	urls := p.mustAPI(t, "GET", fmt.Sprintf("/api/v3/providers/oauth2/%.0f/setup_urls/", provider["pk"]), nil)
	p.issuer, _ = urls["issuer"].(string)
	if !strings.HasPrefix(p.issuer, p.origin+"/") {
		t.Fatal("unexpected fixture issuer")
	}
}

// Drive the real documented flow executor. No management token reaches this
// cookie jar, AppTown, or the token exchange. Unknown stages fail the proof.
func (p *authentikProof) authorize(t *testing.T, start, callbackOrigin string) string {
	t.Helper()
	jar, e := cookiejar.New(nil)
	if e != nil {
		t.Fatal(e)
	}
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	next := start
	method := "GET"
	var payload any
	for step := 0; step < 20; step++ {
		u, e := url.Parse(next)
		if e != nil {
			t.Fatal("invalid provider redirect")
		}
		if u.Scheme+"://"+u.Host == callbackOrigin {
			return u.RequestURI()
		}
		if u.Scheme+"://"+u.Host != p.origin {
			t.Fatal("provider flow left isolated issuer")
		}
		if strings.HasPrefix(u.Path, "/if/flow/") {
			slug := strings.Trim(strings.TrimPrefix(u.Path, "/if/flow/"), "/")
			next = p.origin + "/api/v3/flows/executor/" + url.PathEscape(slug) + "/?query=" + url.QueryEscape(u.RawQuery)
			u, _ = url.Parse(next)
		}
		raw, _ := json.Marshal(payload)
		req, e := http.NewRequestWithContext(t.Context(), method, next, bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Referer", p.origin+"/")
		for _, c := range jar.Cookies(u) {
			if c.Name == "authentik_csrf" || c.Name == "csrftoken" {
				req.Header.Set("X-CSRFToken", c.Value)
			}
		}
		resp, e := client.Do(req)
		if e != nil {
			t.Fatal("provider browser request failed")
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := resp.Header.Get("Location")
			resp.Body.Close()
			ref, e := url.Parse(loc)
			if e != nil {
				t.Fatal("invalid provider location")
			}
			next = u.ResolveReference(ref).String()
			method = "GET"
			payload = nil
			continue
		}
		var challenge map[string]any
		e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&challenge)
		resp.Body.Close()
		if e != nil || resp.StatusCode != 200 {
			t.Fatalf("provider browser flow failed at %s %s status %d (query and body withheld)", method, u.Path, resp.StatusCode)
		}
		switch challenge["component"] {
		case "ak-stage-identification":
			payload = map[string]string{"component": "ak-stage-identification", "uid_field": "proof-person"}
			method = "POST"
		case "ak-stage-password":
			payload = map[string]string{"component": "ak-stage-password", "password": p.password}
			method = "POST"
		case "xak-flow-redirect":
			loc, ok := challenge["to"].(string)
			if !ok {
				t.Fatal("provider redirect lacks destination")
			}
			ref, e := url.Parse(loc)
			if e != nil {
				t.Fatal("invalid provider flow destination")
			}
			next = u.ResolveReference(ref).String()
			method = "GET"
			payload = nil
		default:
			t.Fatalf("unexpected provider flow stage %v", challenge["component"])
		}
	}
	t.Fatal("provider flow exceeded bounded stage count")
	return ""
}

func TestIntegrationExternalSignInWithDisposableAuthentik(t *testing.T) {
	p := startAuthentikProof(t)
	origin := "http://127.0.0.1:51235"
	p.configure(t, origin+"/api/v1/auth/external/callback")
	a, h := internalApp(t, Config{PublicHost: origin})
	w := postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "boss", "correct-horse"))
	if w.Code != 200 {
		t.Fatal("local bootstrap failed")
	}
	local := sessionFrom(t, w)
	if e := a.Secrets.SetInstance(t.Context(), "OIDC_PROOF_SECRET", p.clientSecret, "boss"); e != nil {
		t.Fatal(e)
	}
	request := func(method, path string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, origin+path, bytes.NewReader(raw))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w = request("PUT", "/api/v1/auth/external/provider", identity.ExternalProviderChange{Issuer: p.issuer, DisplayName: "Disposable Authentik", ClientID: "apptown-proof", SecretRef: "OIDC_PROOF_SECRET", AllowSignup: true}, local)
	if w.Code != 200 {
		t.Fatal("AppTown provider save failed")
	}
	var config identity.ExternalProvider
	if e := json.Unmarshal(w.Body.Bytes(), &config); e != nil {
		t.Fatal(e)
	}
	w = request("POST", "/api/v1/auth/external/enabled", httpapi.ExternalEnableRequest{Revision: config.Revision, Enabled: true}, local)
	if w.Code != 200 {
		t.Fatal("AppTown provider enable failed")
	}
	json.Unmarshal(w.Body.Bytes(), &config)
	var firstID string
	var external *http.Cookie
	for attempt := 0; attempt < 2; attempt++ {
		w = request("POST", "/api/v1/auth/external/start", httpapi.ExternalStartRequest{Name: "external-person", ReturnPath: "/"})
		if w.Code != 200 {
			t.Fatal("AppTown external start failed")
		}
		var redirect httpapi.ExternalRedirect
		json.Unmarshal(w.Body.Bytes(), &redirect)
		var binding *http.Cookie
		for _, c := range w.Result().Cookies() {
			if c.Name == "apptown_external" {
				binding = c
			}
		}
		if binding == nil {
			t.Fatal("missing browser binding")
		}
		callback := p.authorize(t, redirect.URL, origin)
		w = request("GET", callback, nil, binding)
		if w.Code != 303 || w.Header().Get("Location") != "/" {
			t.Fatal("AppTown refused real provider callback")
		}
		external = sessionFrom(t, w)
		if external.MaxAge != 3600 || !external.HttpOnly || external.SameSite != http.SameSiteLaxMode {
			t.Fatal("external session lease or cookie protection differs from contract")
		}
		w = request("GET", "/api/v1/auth/me", nil, external)
		var me map[string]any
		json.Unmarshal(w.Body.Bytes(), &me)
		if w.Code != 200 || me["user"] != "external-person" {
			t.Fatal("external session missing")
		}
		principal, e := a.Access.SessionIdentity(t.Context(), external.Value)
		if e != nil || principal.ID.IsZero() {
			t.Fatal("external principal missing")
		}
		if attempt == 0 {
			firstID = principal.ID.String()
		} else if firstID != principal.ID.String() {
			t.Fatal("repeat subject changed principal UUID")
		}
		w = request("GET", callback, nil, binding)
		if w.Code != 303 || w.Header().Get("Location") == "/" {
			t.Fatal("real provider callback replay admitted")
		}
	}
	w = request("POST", "/api/v1/auth/external/enabled", httpapi.ExternalEnableRequest{Revision: config.Revision, Enabled: false}, local)
	if w.Code != 200 {
		t.Fatal("provider disable failed")
	}
	w = request("GET", "/api/v1/auth/me", nil, external)
	var anonymous httpapi.AuthMe
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &anonymous) != nil || anonymous.User != "" {
		t.Fatal("disabled external session was not observed unauthenticated")
	}
	w = request("GET", "/api/v1/auth/me", nil, local)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"user":"boss"`) {
		t.Fatal("local recovery lost after provider disable")
	}
	w = request("POST", "/api/v1/auth/login", map[string]string{"username": "boss", "password": "correct-horse"})
	if w.Code != 200 {
		t.Fatal("fresh local password recovery failed after provider disable")
	}
	recovered := sessionFrom(t, w)
	w = request("GET", "/api/v1/auth/me", nil, recovered)
	var me httpapi.AuthMe
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &me) != nil || me.User != "boss" {
		t.Fatal("fresh local recovery session missing")
	}
}

func TestAuthentikConfigurationWaitsForRequiredBlueprints(t *testing.T) {
	var scopes, authentication atomic.Int32
	var origin string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{"results": []any{map[string]any{"pk": "fixture-record"}}}
		switch {
		case r.URL.Path == "/api/v3/flows/instances/" && r.URL.Query().Get("slug") == "default-authentication-flow":
			rows := []any{}
			if authentication.Add(1) > 1 {
				rows = append(rows, map[string]any{"pk": "authentication-flow"})
			}
			out["results"] = rows
		case strings.Contains(r.URL.Path, "propertymappings/provider/scope"):
			n := scopes.Add(1)
			rows := []any{}
			if n > 1 {
				names := []string{"openid", "profile", "email"}
				if scope := r.URL.Query().Get("scope_name"); scope != "" {
					names = []string{scope}
				}
				for _, name := range names {
					rows = append(rows, map[string]any{"scope_name": name, "pk": "scope-" + name})
				}
			}
			out["results"] = rows
		case strings.HasSuffix(r.URL.Path, "setup_urls/"):
			out = map[string]any{"issuer": origin + "/application/o/proof/"}
		case r.Method == "POST":
			out = map[string]any{"pk": 1}
			if r.URL.Path == "/api/v3/providers/oauth2/" {
				var body struct {
					Mappings       []string `json:"property_mappings"`
					Authentication string   `json:"authentication_flow"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if authentication.Load() < 2 || body.Authentication != "authentication-flow" {
					t.Errorf("provider did not bind the ready authentication flow: requests=%d binding=%q", authentication.Load(), body.Authentication)
				}
				if strings.Join(body.Mappings, ",") != "scope-openid,scope-profile,scope-email" {
					t.Errorf("required mappings = %v", body.Mappings)
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(out); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	origin = server.URL
	p := &authentikProof{origin: origin, client: server.Client()}
	p.configure(t, "http://localhost/callback")
	if scopes.Load() < 4 {
		t.Fatalf("did not observe delayed exact scope records: %d requests", scopes.Load())
	}
}

func TestAuthentikScopeObservationRejectsAmbiguousOrInvalidRecords(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"duplicate", `{"results":[{"pk":"a","scope_name":"openid"},{"pk":"b","scope_name":"openid"}]}`, "ambiguous"},
		{"inventory", `{"results":{}}`, "invalid provider fixture inventory"},
		{"invalid JSON", `{`, "invalid provider fixture inventory"},
		{"record", `{"results":["wrong"]}`, "invalid provider fixture record"},
		{"different scope", `{"results":[{"pk":"a","scope_name":"email"}]}`, "invalid provider fixture scope record"},
		{"missing key", `{"results":[{"scope_name":"openid"}]}`, "invalid provider fixture scope record"},
		{"empty key", `{"results":[{"pk":"","scope_name":"openid"}]}`, "invalid provider fixture scope record"},
		{"wrong key type", `{"results":[{"pk":1,"scope_name":"openid"}]}`, "invalid provider fixture scope record"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Query().Get("scope_name") != "openid" {
					t.Error("scope selection continued after invalid record")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			p := &authentikProof{origin: server.URL, client: server.Client()}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			got, err := p.scopeMappings(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) || got != nil {
				t.Fatalf("mappings=%v error=%v, want %s", got, err, tc.want)
			}
			if calls.Load() != 1 {
				t.Fatalf("invalid response retried: %d", calls.Load())
			}
		})
	}
}

func TestAuthentikScopeObservationHonorsCancellation(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"results":[]}`))
				cancel()
			}))
			defer server.Close()
			p := &authentikProof{origin: server.URL, client: server.Client()}
			got, err := p.scopeMappings(ctx)
			// Cancellation may interrupt response decoding or the polling pause.
			// Both must refuse configuration; neither guarantees an error category.
			if err == nil || ctx.Err() != context.Canceled || got != nil {
				t.Fatalf("mappings=%v error=%v", got, err)
			}
		})
	}
}

func TestAuthentikProofUsesExactRunnerJob(t *testing.T) {
	got, err := authentikProofProject("/tmp/apptown-ci-proof-123")
	if err != nil || got != "apptown-oidc-proof-proof-123" {
		t.Fatalf("project=%q error=%v", got, err)
	}
	for _, dir := range []string{"/tmp/apptown-ci-", "/tmp/apptown-ci-bad job", "/tmp/apptown-ci-bad;job"} {
		if _, err := authentikProofProject(dir); err == nil {
			t.Fatalf("accepted %q", dir)
		}
	}
	a, err := authentikProofProject("/tmp/native")
	if err != nil {
		t.Fatal(err)
	}
	b, err := authentikProofProject("/tmp/native")
	if err != nil {
		t.Fatal(err)
	}
	if a == b || !strings.HasPrefix(a, "apptown-oidc-proof-") {
		t.Fatalf("native identities %q %q", a, b)
	}
}

// The shell finalizer is the subject: exercise its actual argv, environment and
// exit handling through a fake executable, rather than an in-process API fake.
func TestAuthentikProofFinalizerSettlesOnlyItsExactJob(t *testing.T) {
	dir := t.TempDir()
	fake := `#!/bin/sh
set -eu
kind=$1; action=$2
case $kind in container|volume|network) ;; *) exit 30 ;; esac
if [ "$action" = ls ]; then
 if [ "$kind" = container ]; then
  [ "$3" = --all ] || exit 31
  set -- "$1" "$2" "$4" "$5" "$6"
 fi
 [ "$#" = 5 ] && [ "$3" = --quiet ] && [ "$4" = --filter ] || exit 31
 [ "$5" = label=com.docker.compose.project=apptown-oidc-proof-owned-job ] || exit 32
 if [ -f "$STATE/$kind" ]; then printf '%s\n' "owned-$kind"; fi
elif [ "$action" = rm ]; then
 if [ "$kind" = container ]; then [ "$3" = --force ] || exit 33; shift; fi
 [ "$3" = "owned-$kind" ] || exit 34
 if [ "${RETAIN:-}" != "$kind" ]; then rm "$STATE/$kind"; fi
else exit 35
fi
`
	if err := execfile.Write(filepath.Join(dir, "docker"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	neighbor := filepath.Join(dir, "neighbor")
	if err := os.WriteFile(neighbor, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"container", "volume", "network"} {
		if err := os.WriteFile(filepath.Join(dir, kind), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	testinputs.Files(t, "testdata/authentik/settle.sh")
	run := func(job, retain string) execution.Outcome {
		return (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{Argv: []string{"sh", "testdata/authentik/settle.sh", job}, Env: []string{"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"), "STATE=" + dir, "RETAIN=" + retain}, Timeout: 5 * time.Second})
	}
	if out := run("owned-job", ""); !out.Exited0() {
		t.Fatalf("settlement: %s", out.Output)
	}
	for _, kind := range []string{"container", "volume", "network"} {
		if _, err := os.Stat(filepath.Join(dir, kind)); !os.IsNotExist(err) {
			t.Fatalf("%s retained: %v", kind, err)
		}
	}
	if got, err := os.ReadFile(neighbor); err != nil || string(got) != "retained" {
		t.Fatalf("neighbor changed: %q %v", got, err)
	}
	if out := run("owned-job", ""); !out.Exited0() {
		t.Fatal("empty settlement is not idempotent")
	}
	if err := os.WriteFile(filepath.Join(dir, "volume"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if out := run("owned-job", "volume"); out.Exited0() {
		t.Fatal("claimed settlement with retained volume")
	}
	for _, job := range []string{"", "other job", "owned-job;bad"} {
		if out := run(job, ""); out.Exited0() {
			t.Fatalf("accepted invalid job %q", job)
		}
	}
}

// Stopped and failed inventories must cross the real shell process boundary.
func TestAuthentikFinalizerDoesNotConfuseStoppedOrUnknownWithAbsent(t *testing.T) {
	for _, mode := range []string{"stopped", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			fake := `#!/bin/sh
set -eu
kind=$1; action=$2
if [ "$action" = ls ]; then
 all=false
 for arg in "$@"; do [ "$arg" != --all ] || all=true; done
 [ "$kind" = container ] || exit 0
 if [ "$MODE" = unknown ]; then
  if [ -f "$STATE/observed" ]; then exit 41; fi
  touch "$STATE/observed"
 elif [ -f "$STATE/stopped" ] && [ "$all" = true ]; then
  printf '%s\n' stopped-owned
 fi
elif [ "$action" = rm ] && [ "$kind" = container ]; then
 [ "$4" = stopped-owned ] || exit 42
 rm "$STATE/stopped"
else exit 43
fi
`
			if err := execfile.Write(filepath.Join(dir, "docker"), []byte(fake), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "stopped"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			testinputs.Files(t, "testdata/authentik/settle.sh")
			out := (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{Argv: []string{"sh", "testdata/authentik/settle.sh", "owned-job"}, Env: []string{"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"), "STATE=" + dir, "MODE=" + mode}, Timeout: 5 * time.Second})
			if mode == "unknown" {
				if out.Exited0() {
					t.Fatal("failed final inventory was reported absent")
				}
			} else {
				if !out.Exited0() {
					t.Fatalf("stopped settlement failed: %s", out.Output)
				}
				if _, err := os.Stat(filepath.Join(dir, "stopped")); !os.IsNotExist(err) {
					t.Fatal("stopped owned container was left behind")
				}
			}
		})
	}
}
