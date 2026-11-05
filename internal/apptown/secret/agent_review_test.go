package secret

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func previewAgentFixture(t *testing.T) (*Service, context.Context, ids.ID, string) {
	t.Helper()
	db := testDB(t)
	ctx := t.Context()
	id, err := database.Transact(ctx, db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(ctx, tx, time.Now(), project.Spec{Slug: "demo", Name: "Demo"})
	})
	if err != nil {
		t.Fatal(err)
	}
	people := identity.New(db, identity.Config{OpenInstance: true})
	if err := people.SeedAdministrator(ctx, "boss"); err != nil {
		t.Fatal(err)
	}
	ctx = access.WithCaller(ctx, access.Caller{Principal: access.Principal{Kind: "user", Name: "boss", Via: "session"}, Verified: true})
	s := NewService(db, Config{Projects: testSecretProjectScope{db}, DataDir: t.TempDir(), Authority: access.Checked{Authority: people, Projects: project.New(db, people)}})
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, SpecFile), []byte("[project]\nname='demo'\nrevision='1.0'\n[profiles.preview]\nPASSWORD={required=true}\nGOOD={required=true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return s, ctx, id, ws
}

// A key used by any non-preview deployment, even under a different name,
// cannot be enabled. A later non-preview binding revokes delivery too.
func TestInfisicalAgentAccessRequiresAnExclusivePreviewKey(t *testing.T) {
	s, ctx, id, ws := previewAgentFixture(t)
	other, err := database.Transact(ctx, s.Vault.db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(ctx, tx, time.Now(), project.Spec{Slug: "other", Name: "Other"})
	})
	if err != nil {
		t.Fatal(err)
	}
	put := func(dep, name, ref string) {
		t.Helper()
		target := other
		if dep == "preview" {
			target = id
		}
		if err := s.Bindings.Put(ctx, target, Binding{Deployment: dep, Name: name, Store: StoreInfisical, Ref: ref, Version: "infisical", UpdatedBy: "boss"}); err != nil {
			t.Fatal(err)
		}
	}
	put("main", "OTHER_NAME", "SHARED")
	put("preview", "PASSWORD", "SHARED")
	if err := s.SetAgentVisible(ctx, id, "preview", "PASSWORD", true); err == nil {
		t.Fatal("shared Infisical key enabled")
	}
	put("preview", "PASSWORD", "PREVIEW")
	if err := s.SetAgentVisible(ctx, id, "preview", "PASSWORD", true); err != nil {
		t.Fatal(err)
	}
	put("staging", "ANOTHER_NAME", "PREVIEW")
	// A missing provider must not obscure the ownership check.
	selected, err := s.agentBindings(ctx, id)
	if err != nil || len(selected) != 0 {
		t.Fatal("delivery selected a newly shared key", err)
	}
	env, err := s.AgentEnv(ctx, id, ws, AgentLaunch{Kind: "agent", ChangeID: idstest.N(1)})
	if err != nil || len(env) != 0 {
		t.Fatal("shared key delivered", err)
	}
}

func TestRebindingRequiresNewAgentConsent(t *testing.T) {
	for _, store := range []string{StoreInternal, StoreInfisical} {
		t.Run(store, func(t *testing.T) {
			s, ctx, id, _ := previewAgentFixture(t)
			b := Binding{Deployment: "preview", Name: "PASSWORD", Store: store, Ref: Scope("demo", "preview", ""), Version: "1", UpdatedBy: "boss"}
			if store == StoreInfisical {
				b.Ref = "KEY_A"
			}
			if b.Store == StoreInternal {
				if _, err := s.Vault.Put(ctx, b.Ref, b.Name, "value", "boss"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Bindings.Put(ctx, id, b); err != nil {
				t.Fatal(err)
			}
			if err := s.SetAgentVisible(ctx, id, "preview", "PASSWORD", true); err != nil {
				t.Fatal(err)
			}
			b.Version = "2"
			if b.Store == StoreInternal {
				if _, err := s.Vault.Put(ctx, b.Ref, b.Name, "value", "boss"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Bindings.Put(ctx, id, b); err != nil {
				t.Fatal(err)
			}
			rows, err := s.Bindings.For(ctx, id, "preview")
			if err != nil || !rows[b.Name].AgentVisible {
				t.Fatal("same reference lost consent", err)
			}
			b.Ref = "KEY_B"
			b.Version = "1"
			if b.Store == StoreInternal {
				if _, err := s.Vault.Put(ctx, b.Ref, b.Name, "value", "boss"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Bindings.Put(ctx, id, b); err != nil {
				t.Fatal(err)
			}
			rows, err = s.Bindings.For(ctx, id, "preview")
			if err != nil || rows[b.Name].AgentVisible {
				t.Fatal("new reference retained consent", err)
			}
		})
	}
}

// An opted-in value that cannot be resolved is omitted with an explanation;
// a healthy value still reaches the agent when another store fails.
func TestAgentResolutionFailuresDoNotBlockTheSession(t *testing.T) {
	for _, failure := range []string{"missing", "infisical", "declaration", "profile"} {
		t.Run(failure, func(t *testing.T) {
			s, ctx, id, ws := previewAgentFixture(t)
			if _, err := s.BindStored(ctx, id, "demo", "preview", "GOOD", "healthy-value", "boss"); err != nil {
				t.Fatal(err)
			}
			if err := s.SetAgentVisible(ctx, id, "preview", "GOOD", true); err != nil {
				t.Fatal(err)
			}
			b := Binding{Deployment: "preview", Name: "PASSWORD", Store: StoreInternal, Ref: Scope("demo", "preview", ""), Version: "1", UpdatedBy: "boss"}
			if failure == "infisical" {
				b.Store, b.Ref = StoreInfisical, "REMOTE"
			}
			if b.Store == StoreInternal {
				if _, err := s.Vault.Put(ctx, b.Ref, b.Name, "value", "boss"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Bindings.Put(ctx, id, b); err != nil {
				t.Fatal(err)
			}
			if err := s.SetAgentVisible(ctx, id, "preview", "PASSWORD", true); err != nil {
				t.Fatal(err)
			}
			if failure == "missing" {
				if err := s.Vault.Delete(ctx, b.Ref, b.Name); err != nil {
					t.Fatal(err)
				}
			}
			switch failure {
			case "declaration":
				if err := os.WriteFile(filepath.Join(ws, SpecFile), []byte("invalid ["), 0600); err != nil {
					t.Fatal(err)
				}
			case "profile":
				restore := dbtest.NextStatementFailure(s.Vault.db, "-- name: ChosenProfile")
				t.Cleanup(restore)
			}
			env, err := s.AgentEnv(ctx, id, ws, AgentLaunch{Kind: "agent", ChangeID: idstest.N(1)})
			if err != nil {
				t.Fatalf("session blocked: %v", err)
			}
			if failure == "missing" || failure == "infisical" {
				if len(env) != 1 || env[0] != "GOOD=healthy-value" {
					t.Fatal("healthy value omitted")
				}
			} else if len(env) != 0 {
				t.Fatal("invalid source delivered values")
			}
			rows, err := audit.Recent(ctx, s.Vault.db, audit.Filter{ProjectID: id, KindPrefix: "secret.agent-delivery"})
			if err != nil || len(rows) != 1 || !strings.Contains(string(rows[0].Payload), "not given:") || !strings.Contains(string(rows[0].Payload), "PASSWORD") {
				t.Fatal("missing omission evidence", err)
			}
		})
	}
}

// Infisical's provider version, rather than its plaintext or a constant store
// name, invalidates a chat's environment after a refresh.
func TestInfisicalAgentDigestTracksProviderVersions(t *testing.T) {
	s, ctx, id, ws := previewAgentFixture(t)
	if err := os.WriteFile(filepath.Join(s.dataDir, "secrets.env"), []byte("INFISICAL_URL=http://infisical.test\nINFISICAL_CLIENT_ID=id\nINFISICAL_CLIENT_SECRET=credential\nINFISICAL_PROJECT_ID=project\n"), 0600); err != nil {
		t.Fatal(err)
	}
	version := 1
	value := "first-canary"
	s.client = infisicalHTTP(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			return secretResponse(200, `{"accessToken":"token","expiresIn":3600}`)
		}
		return secretResponse(200, fmt.Sprintf(`{"secrets":[{"secretKey":"PREVIEW","secretValue":%q,"version":%d}]}`, value, version))
	})
	if err := s.Bindings.Put(ctx, id, Binding{Deployment: "preview", Name: "PASSWORD", Store: StoreInfisical, Ref: "PREVIEW", Version: "infisical", UpdatedBy: "boss"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentVisible(ctx, id, "preview", "PASSWORD", true); err != nil {
		t.Fatal(err)
	}
	launch := AgentLaunch{Kind: "change-chat", ChangeID: idstest.N(1)}
	first, err := s.PrepareAgentEnv(ctx, id, ws, launch)
	if err != nil || len(first.Env) != 1 || first.Secrets[0].Version != "1" {
		t.Fatal("missing provider version", err)
	}
	version = 2
	value = "second-canary"
	staleSecrets(&s.infisical)
	second, err := s.PrepareAgentEnv(ctx, id, ws, launch)
	if err != nil || len(second.Env) != 1 || second.Env[0] != "PASSWORD=second-canary" || second.Secrets[0].Version != "2" || first.Digest() == second.Digest() {
		t.Fatal("rotation did not change delivery", err)
	}
	// Changing plaintext alone must never influence metadata or its digest.
	second.Env = []string{"PASSWORD=unrelated-plaintext"}
	digest := second.Digest()
	second.Env = []string{"PASSWORD=different-plaintext"}
	if digest != second.Digest() {
		t.Fatal("digest incorporates plaintext")
	}
}

// Workers read declarations at the exact source commit. A broken declaration
// there must omit selected values with evidence, even when the checkout is valid.
func TestAgentCommitResolutionFailureDoesNotBlockTheStep(t *testing.T) {
	s, ctx, id, ws := previewAgentFixture(t)
	if _, err := s.BindStored(ctx, id, "demo", "preview", "PASSWORD", "value", "boss"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentVisible(ctx, id, "preview", "PASSWORD", true); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		out, err := gitexec.HubOutput(ctx, ws, nil, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	if err := os.WriteFile(filepath.Join(ws, SpecFile), []byte("invalid ["), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", SpecFile)
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-m", "Invalid declaration")
	commit := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(ws, SpecFile), []byte("[project]\nname='demo'\nrevision='1.0'\n[profiles.preview]\nPASSWORD={required=true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	env, err := s.AgentEnvAt(ctx, id, ws, commit, AgentLaunch{Kind: "agent", ChangeID: idstest.N(1)})
	if err != nil || len(env) != 0 {
		t.Fatal("invalid committed declaration blocked the step or delivered values", err)
	}
	rows, err := audit.Recent(ctx, s.Vault.db, audit.Filter{ProjectID: id, KindPrefix: "secret.agent-delivery"})
	if err != nil || len(rows) != 1 || !strings.Contains(string(rows[0].Payload), "not given:") {
		t.Fatal("missing omission record", err)
	}
}

type suppliedProjectScope struct {
	slug string
	err  error
}

func (s suppliedProjectScope) Slug(context.Context, ids.ID) (string, error) { return s.slug, s.err }

// The composition root supplies current identity; secret must not read project storage itself.
func TestAgentDeliveryUsesSuppliedProjectScope(t *testing.T) {
	t.Parallel()
	s, ctx, id, ws := previewAgentFixture(t)
	if _, err := s.BindStored(ctx, id, "demo", "preview", "PASSWORD", "preview-value", "boss"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentVisible(ctx, id, "preview", "PASSWORD", true); err != nil {
		t.Fatal(err)
	}
	s.projects = suppliedProjectScope{slug: "different"}
	env, err := s.AgentEnv(ctx, id, ws, AgentLaunch{Kind: "agent", ChangeID: idstest.N(1)})
	if err != nil || len(env) != 0 {
		t.Fatalf("delivery ignored supplied scope: %v, %v", env, err)
	}
	if err := s.SetAgentVisible(ctx, id, "preview", "PASSWORD", true); err == nil {
		t.Fatal("toggle ignored supplied scope")
	}
	unavailable := errors.New("scope unavailable")
	s.projects = suppliedProjectScope{err: unavailable}
	if err := s.SeparatePreview(ctx, id, "PASSWORD"); !errors.Is(err, unavailable) {
		t.Fatalf("scope error lost: %v", err)
	}
}
