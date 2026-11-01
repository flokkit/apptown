package identity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/oidclogin"
	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

type externalFixture struct {
	s                *Service
	admin            access.Principal
	provider         ExternalProvider
	nonce, challenge string
	subject          string
	calls            atomic.Int32
	duringExchange   func()
	now              time.Time
}

func newExternalFixture(t *testing.T) *externalFixture {
	t.Helper()
	f := &externalFixture{subject: "subject-one", now: time.Now().UTC().Truncate(time.Second)}
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "one", Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			f.calls.Add(1)
			r.ParseForm()
			if oauth2.S256ChallengeFromVerifier(r.Form.Get("code_verifier")) != f.challenge {
				http.Error(w, "bad verifier", 400)
				return
			}
			if f.duringExchange != nil {
				f.duringExchange()
			}
			claims := map[string]any{"iss": issuer, "sub": f.subject, "email": "alice@example.invalid", "aud": "client", "exp": f.now.Add(time.Hour).Unix(), "iat": f.now.Unix(), "auth_time": f.now.Unix(), "nonce": f.nonce}
			body, _ := json.Marshal(claims)
			signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "one"))
			if e != nil {
				http.Error(w, "sign", 500)
				return
			}
			signed, e := signer.Sign(body)
			if e != nil {
				http.Error(w, "sign", 500)
				return
			}
			token, _ := signed.CompactSerialize()
			json.NewEncoder(w).Encode(map[string]any{"access_token": "not-retained", "token_type": "Bearer", "id_token": token})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	issuer = server.URL
	f.s = New(testDB(t), Config{Now: func() time.Time { return f.now }, External: &ExternalDependencies{Internal: true, Origin: issuer, Protocol: oidclogin.New(nil, func() time.Time { return f.now }, true), Secret: func(context.Context, string) (string, error) { return "client-secret", nil }}})
	if e := testSaveAccount(f.s.db, accountEdit{Name: "boss", Role: "admin", PasswordHash: admissionPasswordHash}, "setup"); e != nil {
		t.Fatal(e)
	}
	f.admin = admittedPrincipal(t, f.s, "boss")
	if _, e := f.s.EnsureUser(context.Background(), "alice"); e != nil {
		t.Fatal(e)
	}
	p, e := f.s.SaveExternalProvider(context.Background(), ExternalProviderChange{Issuer: issuer, DisplayName: "Example", ClientID: "client", SecretRef: "OIDC_SECRET"}, f.admin)
	if e != nil {
		t.Fatal(e)
	}
	p, e = f.s.SetExternalEnabled(context.Background(), p.Revision, true, f.admin)
	if e != nil {
		t.Fatal(e)
	}
	f.provider = p
	person := admittedPrincipal(t, f.s, "alice")
	if e := f.s.BindExternal(context.Background(), p.ID, person.ID, "subject-one", f.admin); e != nil {
		t.Fatal(e)
	}
	return f
}
func (f *externalFixture) start(t *testing.T) (string, string) {
	t.Helper()
	flow, e := f.s.BeginExternal(context.Background(), ExternalStart{ReturnPath: "/"})
	if e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(flow.Redirect)
	if e != nil {
		t.Fatal(e)
	}
	f.nonce = u.Query().Get("nonce")
	f.challenge = u.Query().Get("code_challenge")
	return u.Query().Get("state"), flow.BrowserSecret
}
func TestExternalSessionRevocationReachesDeferredAuthority(t *testing.T) {
	f := newExternalFixture(t)
	ctx := context.Background()
	state, verifier := f.start(t)
	signed, e := f.s.CompleteExternal(ctx, state, verifier, "code", nil)
	if e != nil {
		t.Fatal(e)
	}
	if signed.Principal.Name != "alice" || signed.Lifetime != time.Hour {
		t.Fatalf("unexpected session %+v", signed)
	}
	if _, e := f.s.Grant(ctx, Spec{SubjectType: "user", Subject: "alice", Capability: string(access.CapProjectRead), Scope: access.ScopeInstance, By: "setup"}, ""); e != nil {
		t.Fatal(e)
	}
	permission := access.Permission{Capability: access.CapProjectRead, Resource: access.Resource{Type: "project", ID: idstest.N(7).String(), ProjectID: idstest.N(7)}}
	if e := f.s.RequireStill(ctx, signed.Principal, permission); e != nil {
		t.Fatal(e)
	}
	local, e := issueSessionIn(ctx, f.s.db, "alice", time.Hour, f.now)
	if e != nil {
		t.Fatal(e)
	}
	cli, e := testIssueToken(f.s, "external-independent-cli", "alice", "member", "setup")
	if e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.SetExternalEnabled(ctx, f.provider.Revision, false, f.admin); e != nil {
		t.Fatal(e)
	}
	if got, e := f.s.SessionIdentity(ctx, signed.Token); e != nil || got.Name != "" {
		t.Fatalf("disabled provider browser admitted %+v %v", got, e)
	}
	if e := f.s.RequireStill(ctx, signed.Principal, permission); !errors.Is(e, errCredentialGone) {
		t.Fatalf("disabled provider deferred authority: %v", e)
	}
	if got, e := f.s.SessionIdentity(ctx, local); e != nil || got.Name != "alice" {
		t.Fatalf("independent local session lost: %+v %v", got, e)
	}
	if got, e := f.s.PersonalTokenIdentity(ctx, cli, nil); e != nil || got.Name != "alice" {
		t.Fatalf("independent CLI lost: %+v %v", got, e)
	}
	if _, e := f.s.CompleteExternal(ctx, state, verifier, "code", nil); e == nil || f.calls.Load() != 1 {
		t.Fatal("callback replay repeated the exchange")
	}
}
func TestExternalCallbackRechecksRevisionAndRollsBackSessionFailures(t *testing.T) {
	for _, kind := range []string{"revision", "session", "provenance", "receipt"} {
		t.Run(kind, func(t *testing.T) {
			f := newExternalFixture(t)
			state, verifier := f.start(t)
			ctx := context.Background()
			restore := func() {}
			switch kind {
			case "revision":
				f.duringExchange = func() {
					if _, e := f.s.SetExternalEnabled(ctx, f.provider.Revision, false, f.admin); e != nil {
						t.Error(e)
					}
				}
			case "session":
				restore = dbtest.NextStatementFailure(f.s.db, "-- name: InsertSession")
			case "provenance":
				restore = dbtest.NextStatementFailure(f.s.db, "-- name: InsertExternalSession")
			case "receipt":
				restore = dbtest.NextStatementFailure(f.s.db, "-- name: AppendReceipt")
			}
			defer restore()
			got, e := f.s.CompleteExternal(ctx, state, verifier, "code", nil)
			if e == nil || got.Token != "" {
				t.Fatalf("%s issued session %+v err=%v", kind, got, e)
			}
			var n int
			if e := f.s.db.SQL().QueryRowContext(context.Background(), "SELECT count(*) FROM sessions WHERE principal_id=$1", admittedPrincipal(t, f.s, "alice").ID).Scan(&n); e != nil || n != 0 {
				t.Fatalf("failed callback persisted sessions=%d err=%v", n, e)
			}
		})
	}
}
func TestExternalProviderRequiresPersistentLocalRecovery(t *testing.T) {
	f := newExternalFixture(t)
	ctx := context.Background()
	if e := f.s.SeedAdministrator(ctx, "external-admin"); e != nil {
		t.Fatal(e)
	}
	disabled := true
	if e := f.s.SaveUser(ctx, AccountChange{Username: "boss", Disabled: &disabled}, f.admin); !errors.Is(e, errLocalRecovery) {
		t.Fatalf("disabled only local password root: %v", e)
	}
	if e := f.s.SaveUser(ctx, AccountChange{Username: "boss", Role: "member"}, f.admin); !errors.Is(e, errLocalRecovery) {
		t.Fatalf("demoted only local password root: %v", e)
	}
	member := admittedPrincipal(t, f.s, "alice")
	if _, e := f.s.SetExternalEnabled(ctx, f.provider.Revision, false, member); e == nil {
		t.Fatal("ordinary member changed provider")
	}
}

func TestExternalCallbackHasOneWinnerAndRejectsBrowserSubstitution(t *testing.T) {
	f := newExternalFixture(t)
	ctx := context.Background()
	state, verifier := f.start(t)
	if _, e := f.s.CompleteExternal(ctx, state, "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", "code", nil); !errors.Is(e, oidclogin.ErrRejected) {
		t.Fatalf("browser substitution: %v", e)
	}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, e := f.s.CompleteExternal(ctx, state, verifier, "code", nil); results <- e }()
	}
	successes := 0
	for i := 0; i < 2; i++ {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 || f.calls.Load() != 1 {
		t.Fatalf("callbacks issued %d sessions, exchanged %d times", successes, f.calls.Load())
	}
}
func TestExternalBindingAndAccountRevocationDuringExchangeRefuseAdmission(t *testing.T) {
	for _, kind := range []string{"binding", "account"} {
		t.Run(kind, func(t *testing.T) {
			f := newExternalFixture(t)
			ctx := context.Background()
			state, verifier := f.start(t)
			f.duringExchange = func() {
				if kind == "binding" {
					bs, e := f.s.ExternalBindings(ctx, f.admin, true)
					if e != nil {
						t.Error(e)
						return
					}
					if e := f.s.RevokeExternalBinding(ctx, bs[0].ID, f.admin); e != nil {
						t.Error(e)
					}
				} else {
					disabled := true
					if e := f.s.SaveUser(ctx, AccountChange{Username: "alice", Disabled: &disabled}, f.admin); e != nil {
						t.Error(e)
					}
				}
			}
			if signed, e := f.s.CompleteExternal(ctx, state, verifier, "code", nil); e == nil || signed.Token != "" {
				t.Fatalf("%s revoked during exchange admitted %+v %v", kind, signed, e)
			}
		})
	}
}
func TestExternalBrowserTransactionsAndSessionsExpire(t *testing.T) {
	f := newExternalFixture(t)
	ctx := context.Background()
	state, verifier := f.start(t)
	f.now = f.now.Add(11 * time.Minute)
	if _, e := f.s.CompleteExternal(ctx, state, verifier, "code", nil); !errors.Is(e, oidclogin.ErrRejected) || f.calls.Load() != 0 {
		t.Fatalf("expired flow exchanged: %v", e)
	}
	f.now = f.now.Add(-11 * time.Minute)
	state, verifier = f.start(t)
	signed, e := f.s.CompleteExternal(ctx, state, verifier, "code", nil)
	if e != nil {
		t.Fatal(e)
	}
	f.now = f.now.Add(time.Hour + time.Second)
	if got, e := f.s.SessionIdentity(ctx, signed.Token); e != nil || got.Name != "" {
		t.Fatalf("expired external session retained: %+v %v", got, e)
	}
	if _, e := f.s.stillPrincipal(ctx, signed.Principal); !errors.Is(e, errCredentialGone) {
		t.Fatalf("expired deferred credential retained: %v", e)
	}
}

func TestExternalReplacementRejectsPreviousConnectionEdits(t *testing.T) {
	for _, operation := range []string{"enable", "save"} {
		t.Run(operation, func(t *testing.T) {
			f := newExternalFixture(t)
			ctx := context.Background()
			change := ExternalProviderChange{Revision: f.provider.Revision, Issuer: f.provider.Issuer, DisplayName: "First replacement", ClientID: "replacement-one", SecretRef: "OIDC_SECRET"}
			first, err := f.s.SaveExternalProvider(ctx, change, f.admin)
			if err != nil {
				t.Fatal(err)
			}
			change.Revision = first.Revision
			change.ClientID = "replacement-two"
			second, err := f.s.SaveExternalProvider(ctx, change, f.admin)
			if err != nil {
				t.Fatal(err)
			}
			if first.ID == second.ID {
				t.Fatal("replacement reused connection identity")
			}
			if operation == "enable" {
				_, err = f.s.SetExternalEnabled(ctx, first.Revision, true, f.admin)
			} else {
				change.ClientID = first.ClientID
				change.Revision = first.Revision
				_, err = f.s.SaveExternalProvider(ctx, change, f.admin)
			}
			if !errors.Is(err, errExternalChanged) {
				t.Fatalf("stale %s against replaced connection must conflict, got %v", operation, err)
			}
			current, err := f.s.ExternalProvider(ctx)
			if err != nil || current == nil || *current != second {
				t.Fatalf("stale edit changed replacement: got %+v err=%v want %+v", current, err, second)
			}

		})
	}
}

func TestExternalSubjectsNeverAutoLinkByNamesOrEmail(t *testing.T) {
	f := newExternalFixture(t)
	ctx := context.Background()
	f.subject = "different-subject"
	flow, e := f.s.BeginExternal(ctx, ExternalStart{Name: "alice"})
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(flow.Redirect)
	f.nonce = u.Query().Get("nonce")
	f.challenge = u.Query().Get("code_challenge")
	if _, e := f.s.CompleteExternal(ctx, u.Query().Get("state"), flow.BrowserSecret, "code", nil); !errors.Is(e, ErrExternalUnbound) {
		t.Fatalf("unbound subject linked by name/email: %v", e)
	}
	p, e := f.s.SaveExternalProvider(ctx, ExternalProviderChange{Revision: f.provider.Revision, Issuer: f.provider.Issuer, ClientID: f.provider.ClientID, DisplayName: "Signup", SecretRef: "OIDC_SECRET", AllowSignup: true}, f.admin)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.SetExternalEnabled(ctx, p.Revision, true, f.admin); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"alice", "new-person"} {
		flow, e = f.s.BeginExternal(ctx, ExternalStart{Name: name})
		if e != nil {
			t.Fatal(e)
		}
		u, _ = url.Parse(flow.Redirect)
		f.nonce = u.Query().Get("nonce")
		f.challenge = u.Query().Get("code_challenge")
		signed, e := f.s.CompleteExternal(ctx, u.Query().Get("state"), flow.BrowserSecret, "code", nil)
		if name == "alice" {
			if e == nil {
				t.Fatal("explicit signup took over existing name")
			}
		} else if e != nil || signed.Principal.Name != name || signed.Principal.ID == admittedPrincipal(t, f.s, "alice").ID {
			t.Fatalf("new subject not independently created: %+v %v", signed, e)
		}
	}
}

func TestExternalSelfLinkRequiresLocalPasswordSessionAtBothBoundaries(t *testing.T) {
	for _, mode := range []string{"valid", "wrong password", "revoked during exchange", "external session"} {
		t.Run(mode, func(t *testing.T) {
			f := newExternalFixture(t)
			ctx := t.Context()
			local, e := f.s.SignIn(ctx, PasswordLogin{Username: "boss", Password: "admission-password"})
			if e != nil {
				t.Fatal(e)
			}
			pr, e := f.s.SessionIdentity(ctx, local.Token)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "external session" {
				state, verifier := f.start(t)
				signed, e := f.s.CompleteExternal(ctx, state, verifier, "code", nil)
				if e != nil {
					t.Fatal(e)
				}
				pr, e = f.s.SessionIdentity(ctx, signed.Token)
				if e != nil {
					t.Fatal(e)
				}
			}
			f.subject = "new-linked-subject"
			password := "admission-password"
			if mode == "wrong password" {
				password = "wrong"
			}
			flow, e := f.s.BeginExternal(ctx, ExternalStart{Link: true, Principal: pr, Password: password, ReturnPath: "/account"})
			if mode == "wrong password" || mode == "external session" {
				if e == nil {
					t.Fatal("link admitted without fresh local password session")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			u, e := url.Parse(flow.Redirect)
			if e != nil {
				t.Fatal(e)
			}
			if u.Query().Get("max_age") != "300" {
				t.Fatal("self-link did not request fresh provider authentication")
			}
			f.nonce = u.Query().Get("nonce")
			f.challenge = u.Query().Get("code_challenge")
			if mode == "revoked during exchange" {
				f.duringExchange = func() {
					if e := f.s.sessions.end(ctx, local.Token); e != nil {
						t.Fatal(e)
					}
				}
			}
			signed, e := f.s.CompleteExternal(ctx, u.Query().Get("state"), flow.BrowserSecret, "code", nil)
			if mode == "revoked during exchange" {
				if e == nil || signed.Token != "" {
					t.Fatal("revoked local linking session admitted")
				}
				return
			}
			if e != nil || signed.Principal.ID != pr.ID || signed.ReturnPath != "/account" {
				t.Fatalf("self-link result: %+v %v", signed.Principal, e)
			}
			bindings, e := f.s.ExternalBindings(ctx, pr, false)
			if e != nil || len(bindings) != 1 {
				t.Fatalf("committed self-link missing: count=%d err=%v", len(bindings), e)
			}
		})
	}
}

func TestExternalAdministratorAdmissionUsesCurrentNetworkPolicy(t *testing.T) {
	f := newExternalFixture(t)
	ctx := t.Context()
	if e := f.s.SeedAdministrator(ctx, "alice"); e != nil {
		t.Fatal(e)
	}
	state, verifier := f.start(t)
	f.duringExchange = func() {
		if _, e := f.s.SetAdministratorNetworks(ctx, f.admin, &AdministratorNetworks{}, AdministratorNetworks{"10.0.0.0/8"}, net.ParseIP("10.1.2.3")); e != nil {
			t.Fatal(e)
		}
	}
	signed, e := f.s.CompleteExternal(ctx, state, verifier, "code", net.ParseIP("192.0.2.1"))
	var denied *AdministratorDenied
	if !errors.As(e, &denied) || signed.Token != "" {
		t.Fatalf("current administrator network not enforced: %v", e)
	}
	f.duringExchange = nil
	state, verifier = f.start(t)
	signed, e = f.s.CompleteExternal(ctx, state, verifier, "code", net.ParseIP("10.1.2.3"))
	if e != nil || signed.Principal.Role != "admin" {
		t.Fatalf("permitted administrator refused: %v", e)
	}
}

func TestExternalRecoveryRequiresPersistentLocalGroupAuthority(t *testing.T) {
	for _, mode := range []string{"membership", "group", "grant", "expiring activation"} {
		t.Run(mode, func(t *testing.T) {
			f := newExternalFixture(t)
			ctx := t.Context()
			if e := f.s.SeedAdministrator(ctx, "passwordless-backup"); e != nil {
				t.Fatal(e)
			}
			backup := admittedPrincipal(t, f.s, "passwordless-backup")
			if e := f.s.SaveGroup(ctx, GroupChange{Name: "local-recovery", Members: []string{"boss"}}, f.admin); e != nil {
				t.Fatal(e)
			}
			spec := Spec{SubjectType: "group", Subject: "local-recovery", Role: access.RoleInstanceAdmin, Scope: access.ScopeInstance, By: "boss"}
			grant, e := f.s.Grant(ctx, spec, "")
			if e != nil {
				t.Fatal(e)
			}

			direct, e := f.s.Grant(ctx, Spec{SubjectType: "user", Subject: "boss", Role: access.RoleInstanceAdmin, Scope: access.ScopeInstance, By: "passwordless-backup"}, "")
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.s.Revoke(ctx, direct, "passwordless-backup"); e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "membership":
				e = f.s.SaveGroup(ctx, GroupChange{Name: "local-recovery"}, backup)
			case "group":
				e = f.s.DeleteGroup(ctx, "local-recovery", backup)
			case "grant":
				_, e = f.s.Revoke(ctx, grant, "passwordless-backup")
			case "expiring activation":
				p, err := f.s.SetExternalEnabled(ctx, f.provider.Revision, false, backup)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.s.Grant(ctx, spec, f.now.Add(time.Hour).Format(time.RFC3339))
				if err != nil {
					t.Fatal(err)
				}
				_, e = f.s.SetExternalEnabled(ctx, p.Revision, true, backup)
			}
			if !errors.Is(e, errLocalRecovery) {
				t.Fatalf("sole persistent local route lost (%s): %v", mode, e)
			}
		})
	}
}
