package oidclogin

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

func TestSignedIdentityRequiresTransactionAndClientEvidence(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	var issuer string
	var token string
	var exchangeVerifier string
	var keysUnavailable bool
	var wrongSigningKey bool
	var tokenRedirect string
	var keysStarted chan struct{}
	var releaseKeys chan struct{}
	var keysFinished chan struct{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			if keysStarted != nil {
				started, release, finished := keysStarted, releaseKeys, keysFinished
				close(started)
				<-release
				defer close(finished)
			}
			if keysUnavailable {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "one", Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			if tokenRedirect != "" {
				http.Redirect(w, r, tokenRedirect, http.StatusTemporaryRedirect)
				return
			}
			r.ParseForm()
			exchangeVerifier = r.Form.Get("code_verifier")
			json.NewEncoder(w).Encode(map[string]any{"access_token": "discard-me", "token_type": "Bearer", "id_token": token})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL
	client := New(nil, func() time.Time { return now }, true)
	cfg := Configuration{Issuer: issuer, ClientID: "client", Secret: "private", Callback: issuer + "/callback"}
	verifier := oauth2.GenerateVerifier()
	auth, err := client.Begin(context.Background(), cfg, "state", "nonce", verifier, true)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(auth)
	if u.Query().Get("code_challenge") != oauth2.S256ChallengeFromVerifier(verifier) || u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("max_age") != "300" || u.Query().Get("nonce") != "nonce" {
		t.Fatal("authorization lacks transaction/freshness evidence")
	}
	for _, tc := range []struct {
		name  string
		edit  func(map[string]any)
		fresh bool
		ok    bool
	}{
		{"valid", func(map[string]any) {}, true, true},
		{"rotated signing key", func(map[string]any) {
			next, e := rsa.GenerateKey(rand.Reader, 2048)
			if e != nil {
				t.Fatal(e)
			}
			key = next
		}, false, true},
		{"signature", func(map[string]any) { wrongSigningKey = true }, false, false},
		{"nonce", func(c map[string]any) { c["nonce"] = "other" }, false, false},
		{"issuer", func(c map[string]any) { c["iss"] = "https://wrong.example" }, false, false},
		{"audience", func(c map[string]any) { c["aud"] = "other" }, false, false},
		{"authorized party", func(c map[string]any) { c["azp"] = "other" }, false, false},
		{"multiple audiences without party", func(c map[string]any) { c["aud"] = []string{"client", "other"} }, false, false},
		{"empty subject", func(c map[string]any) { c["sub"] = "" }, false, false},
		{"expired", func(c map[string]any) { c["exp"] = now.Add(-time.Hour).Unix() }, false, false},
		{"new token old human", func(c map[string]any) { c["auth_time"] = now.Add(-time.Hour).Unix() }, true, false},
		{"renewal accepts existing SSO", func(c map[string]any) { c["auth_time"] = now.Add(-time.Hour).Unix() }, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wrongSigningKey = false
			claims := map[string]any{"iss": issuer, "sub": "stable-subject", "aud": "client", "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "auth_time": now.Unix(), "nonce": "nonce"}
			tc.edit(claims)
			body, _ := json.Marshal(claims)
			signingKey := key
			if wrongSigningKey {
				other, e := rsa.GenerateKey(rand.Reader, 2048)
				if e != nil {
					t.Fatal(e)
				}
				signingKey = other
			}
			signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: signingKey}, (&jose.SignerOptions{}).WithHeader("kid", "one"))
			if e != nil {
				t.Fatal(e)
			}
			signed, e := signer.Sign(body)
			if e != nil {
				t.Fatal(e)
			}
			token, e = signed.CompactSerialize()
			if e != nil {
				t.Fatal(e)
			}
			got, e := client.Complete(context.Background(), cfg, "code", "nonce", verifier, tc.fresh)
			if tc.ok {
				if e != nil || got.Subject != "stable-subject" || got.Issuer != issuer {
					t.Fatalf("identity=%+v err=%v", got, e)
				}
			} else if !errors.Is(e, ErrRejected) {
				t.Fatalf("expected refusal, got %+v %v", got, e)
			}
			if exchangeVerifier != verifier {
				t.Fatal("exchange lost PKCE verifier")
			}
		})
	}
	t.Run("JWKS outage is unavailable", func(t *testing.T) {
		keysUnavailable = true
		defer func() { keysUnavailable = false }()
		_, e := client.Complete(t.Context(), cfg, "code", "nonce", verifier, false)
		if !errors.Is(e, ErrUnavailable) {
			t.Fatalf("JWKS outage misclassified: %v", e)
		}
	})
	t.Run("token redirect cannot disclose credential", func(t *testing.T) {
		hits := 0
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(500) }))
		defer other.Close()
		tokenRedirect = other.URL
		defer func() { tokenRedirect = "" }()
		_, e := client.Complete(t.Context(), cfg, "code", "nonce", verifier, false)
		if e == nil || hits != 0 {
			t.Fatalf("redirect admitted or followed: err=%v hits=%d", e, hits)
		}
	})
	t.Run("cancel while keys are in flight", func(t *testing.T) {
		keysStarted = make(chan struct{})
		releaseKeys = make(chan struct{})
		keysFinished = make(chan struct{})
		defer func() { keysStarted = nil; releaseKeys = nil; keysFinished = nil }()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, e := client.Complete(ctx, cfg, "code", "nonce", verifier, false); done <- e }()
		select {
		case <-keysStarted:
		case <-time.After(time.Second):
			close(releaseKeys)
			t.Fatal("JWKS request did not start")
		}
		cancel()
		select {
		case e := <-done:
			if !errors.Is(e, ErrUnavailable) {
				close(releaseKeys)
				t.Fatalf("in-flight cancellation: %v", e)
			}
		case <-time.After(time.Second):
			close(releaseKeys)
			t.Fatal("cancellation waited for shared JWKS request")
		}
		close(releaseKeys)
		<-keysFinished
		// Wait for the independently bounded HTTP fetch to finish before changing
		// fixture fields read by its handler.
	})

	t.Run("canceled exchange is unavailable", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, e := client.Complete(ctx, cfg, "code", "nonce", verifier, false)
		if !errors.Is(e, ErrUnavailable) {
			t.Fatalf("cancellation misclassified: %v", e)
		}
	})
}
func TestDiscoveryDoesNotFollowRedirectOrReadUnboundedBody(t *testing.T) {
	hits := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.Write([]byte("{}")) }))
	defer destination.Close()
	for _, mode := range []string{"redirect", "large"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					http.Redirect(w, r, destination.URL, http.StatusFound)
				} else {
					w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
				}
			}))
			defer server.Close()
			c := New(nil, nil, true)
			err := c.Inspect(context.Background(), Configuration{Issuer: server.URL, ClientID: "c", Callback: server.URL + "/cb"})
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("got %v", err)
			}
			if hits != 0 {
				t.Fatal("followed provider redirect")
			}
		})
	}
}

// Provider token headers and JWKS pass through this dependency parser. Keep
// fixtures cheap: public moduli require no RSA validation, and private factors
// below are tiny. This guards the dependency bounds without a DoS payload.
func TestProviderJWKParsingBoundsRSAParameters(t *testing.T) {
	for _, bits := range []int{4096, 4097} {
		n := new(big.Int).Lsh(big.NewInt(1), uint(bits-1))
		raw, err := json.Marshal(map[string]string{
			"kty": "RSA", "n": base64.RawURLEncoding.EncodeToString(n.Bytes()), "e": "AQAB",
		})
		if err != nil {
			t.Fatal(err)
		}
		var key jose.JSONWebKey
		err = json.Unmarshal(raw, &key)
		if bits == 4096 && err != nil {
			t.Fatalf("modulus at limit rejected: %v", err)
		}
		if bits == 4097 && (err == nil || !strings.Contains(err.Error(), "modulus too large")) {
			t.Errorf("oversize modulus must hit size guard, got %v", err)
		}
	}
	var key jose.JSONWebKey
	err := json.Unmarshal([]byte(`{"kty":"RSA","n":"Dw","e":"AQAB","d":"Aw","p":"EQ","q":"Aw"}`), &key)
	// n=15, p=17, q=3: reject inconsistent factors before RSA validation.
	if err == nil || !strings.Contains(err.Error(), "p * q != n") {
		t.Fatalf("inconsistent primes must hit early guard, got %v", err)
	}
}
