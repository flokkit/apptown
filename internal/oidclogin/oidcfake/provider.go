// Package oidcfake supplies a disposable signed external issuer for adapter and
// cross-owner tests. It owns no AppTown state and never uses real credentials.
package oidcfake

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

// Provider performs authorization code/PKCE exchange with signed ID tokens.
type Provider struct {
	URL    string
	server *httptest.Server
	key    *rsa.PrivateKey
	now    func() time.Time
	mu     sync.Mutex
	codes  map[string]code
	serial int
}
type code struct{ nonce, challenge, callback string }

func New(t testing.TB, now func() time.Time) *Provider {
	t.Helper()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	if now == nil {
		now = time.Now
	}
	p := &Provider{key: key, now: now, codes: map[string]code{}}
	p.server = httptest.NewServer(http.HandlerFunc(p.serve))
	p.URL = p.server.URL
	t.Cleanup(p.server.Close)
	return p
}
func (p *Provider) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		json.NewEncoder(w).Encode(map[string]any{"issuer": p.URL, "authorization_endpoint": p.URL + "/authorize", "token_endpoint": p.URL + "/token", "jwks_uri": p.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
	case "/keys":
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &p.key.PublicKey, KeyID: "one", Algorithm: "RS256", Use: "sig"}}})
	case "/authorize":
		q := r.URL.Query()
		if q.Get("client_id") != "client" || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" {
			http.Error(w, "invalid authorization", 400)
			return
		}
		callback, e := url.Parse(q.Get("redirect_uri"))
		if e != nil || callback.Host == "" {
			http.Error(w, "invalid callback", 400)
			return
		}
		p.mu.Lock()
		p.serial++
		key := fmt.Sprintf("one-use-%d", p.serial)
		p.codes[key] = code{q.Get("nonce"), q.Get("code_challenge"), callback.String()}
		p.mu.Unlock()
		v := callback.Query()
		v.Set("state", q.Get("state"))
		v.Set("code", key)
		callback.RawQuery = v.Encode()
		http.Redirect(w, r, callback.String(), http.StatusFound)
	case "/token":
		if e := r.ParseForm(); e != nil {
			http.Error(w, "invalid form", 400)
			return
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "client" || password != "client-secret" {
			http.Error(w, "invalid client", http.StatusUnauthorized)
			return
		}
		p.mu.Lock()
		c, ok := p.codes[r.Form.Get("code")]
		delete(p.codes, r.Form.Get("code"))
		p.mu.Unlock()
		if !ok || r.Form.Get("redirect_uri") != c.callback || oauth2.S256ChallengeFromVerifier(r.Form.Get("code_verifier")) != c.challenge {
			http.Error(w, "invalid code", 400)
			return
		}
		now := p.now()
		claims := map[string]any{"iss": p.URL, "sub": "synthetic-person", "aud": "client", "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "auth_time": now.Unix(), "nonce": c.nonce, "email": "untrusted-name@example.invalid"}
		body, e := json.Marshal(claims)
		if e != nil {
			http.Error(w, "encode", 500)
			return
		}
		signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.key}, (&jose.SignerOptions{}).WithHeader("kid", "one"))
		if e != nil {
			http.Error(w, "sign", 500)
			return
		}
		signed, e := signer.Sign(body)
		if e != nil {
			http.Error(w, "sign", 500)
			return
		}
		raw, e := signed.CompactSerialize()
		if e != nil {
			http.Error(w, "serialize", 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "not-retained", "token_type": "Bearer", "id_token": raw})
	default:
		http.NotFound(w, r)
	}
}
