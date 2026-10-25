package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/stock"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func runtimeFixture(t *testing.T) (*Service, ids.ID, string) {
	t.Helper()
	db := testDB(t)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "demo", Name: "Demo"})
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	kinds := dependency.NewCatalog(db, nil)
	if _, _, err := kinds.SyncStock(t.Context(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	return NewService(db, Dependencies{Kinds: kinds, Projects: project.New(db, nil), Secrets: secret.NewService(db, secret.Config{DataDir: dir})}), id, dir
}

// kindsWith is a catalog whose error-reporting definition injects inject.
type kindsWith map[string]string

func (k kindsWith) ByCapability(_ context.Context, capability string) (*dependency.Kind, error) {
	raw := "capability: " + capability + "\ntitle: t\nregister:\n  connector: glitchtip\n  adapter: glitchtip-project\n  inject:\n"
	for name, tmpl := range k {
		raw += "    " + name + ": \"" + tmpl + "\"\n"
	}
	return dependency.Parse(raw)
}

// What an app with error reporting is given is its stock definition's
// inject list, rendered with the attached DSN; a browser is given only the
// entries that carry the DSN, in its public form. A new entry in the data
// reaches apps with no engine change.
//
// Kills: variable names hard-coded in the engine beside a definition that
// listed different ones.
func TestErrorReportingGivesWhatItsDefinitionLists(t *testing.T) {
	t.Parallel()
	s, id, _ := runtimeFixture(t)
	if got, err := s.ErrorsEnvironment(context.Background(), id); err != nil || got != nil {
		t.Fatalf("an unattached project is given nothing: %v", got)
	}
	if err := s.Attachments.Attach(t.Context(), id, "errors", "", `{"dsn":"https://k:secret@errors.example/3"}`, Entered, "test", nil); err != nil {
		t.Fatal(err)
	}
	stockList := strings.Join(mustErrorsEnvironment(t, s, id), "\n")
	if stockList != "SENTRY_DSN=https://k:secret@errors.example/3" {
		t.Fatalf("the stock definition's list: %q", stockList)
	}
	s.kinds = kindsWith{"SENTRY_DSN": "${dsn}", "MY_ERRORS_URL": "${dsn}", "SENTRY_ENVIRONMENT": "production"}
	if got := strings.Join(mustErrorsEnvironment(t, s, id), "\n"); got != "MY_ERRORS_URL=https://k:secret@errors.example/3\nSENTRY_DSN=https://k:secret@errors.example/3\nSENTRY_ENVIRONMENT=production" {
		t.Fatalf("a changed definition: %q", got)
	}
	browser, err := s.BrowserEnvironment(context.Background(), id)
	if err != nil || len(browser) != 2 || browser["MY_ERRORS_URL"] != "https://k@errors.example/3" || browser["SENTRY_DSN"] != "https://k@errors.example/3" {
		t.Fatalf("the browser gets only the DSN entries, public: %v %v", browser, err)
	}
}

func TestRegistryCredentialsFollowTheCurrentAttachment(t *testing.T) {
	t.Parallel()
	s, id, _ := runtimeFixture(t)
	if mustRegistry(t, s, id) != nil {
		t.Fatal("unattached project has credentials")
	}
	if _, err := s.Bindings.Upsert(t.Context(), "registry", "container-registry", "direct", `{"url":"registry.example"}`, false); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"first-private-token", "rotated-private-token"} {
		resource := `{"repo":"registry.example/team/demo","username":"robot","token":"` + token + `"}`
		if err := s.Attachments.Attach(t.Context(), id, "container-registry", "registry", resource, Entered, "test", nil); err != nil {
			t.Fatal(err)
		}
		got := mustRegistry(t, s, id)
		if got == nil || got.Token != token || got.Host != "registry.example" || got.Repo != "registry.example/team/demo" {
			t.Fatal("credential snapshot did not follow current resource")
		}
	}
	if err := s.Attachments.Attach(t.Context(), id, "container-registry", "missing", `{"repo":"fallback.example/team/demo","username":"robot","token":"private"}`, Entered, "test", nil); err != nil {
		t.Fatal(err)
	}
	if got := mustRegistry(t, s, id); got == nil || got.Host != "fallback.example" {
		t.Fatal("repo host fallback changed")
	}
	// Detaching changes what the project may reach: without its receipt it
	// does not happen.
	restore := s.Attachments.db.FailNextStatement("-- name: AppendReceipt")
	err := s.Attachments.Detach(context.Background(), id, "container-registry", "test")
	restore()
	if err == nil {
		t.Fatal("a detach whose receipt failed reported success")
	}
	if got := mustRegistry(t, s, id); got == nil || got.Host != "fallback.example" {
		t.Fatal("a detach whose receipt failed removed the credential")
	}
	if err := s.Attachments.Detach(context.Background(), id, "container-registry", "test"); err != nil {
		t.Fatal(err)
	}
	if mustRegistry(t, s, id) != nil {
		t.Fatal("detached credential stayed usable")
	}
}

func TestEnvironmentContributionsResolveInTheirOwner(t *testing.T) {
	t.Parallel()
	s, id, dir := runtimeFixture(t)
	spec := `capability: mailer
title: Mail
env:
  - {key: MAIL_HOST, value: "{{connector.host}}"}
  - {key: MAIL_FROM, value: "{{project.slug}}@{{resource.domain}}"}
  - {key: API_KEY, value: "{{secret:resource.key}}"}
  - {key: UNRESOLVED, value: "{{resource.missing}}"}`
	if err := s.ServiceKinds.Save(context.Background(), "mailer", spec, "Mail", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bindings.Upsert(t.Context(), "smtp", "mailer", "service", `{"host":"smtp.example"}`, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Attachments.Attach(t.Context(), id, "mailer", "smtp", `{"domain":"example.com","key":"MAIL_KEY"}`, Entered, "test", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Attachments.Attach(t.Context(), id, "errors", "", `{"dsn":"test-dsn"}`, Entered, "test", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Attachments.Attach(t.Context(), id, "feedback", "", `{"url":"https://feedback.example","appKey":"test-key"}`, Entered, "test", nil); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"first-secret", "rotated-secret"} {
		if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("MAIL_KEY="+value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		got := strings.Join(mustEnvironment(t, s, id), "\n")
		for _, want := range []string{"MAIL_HOST=smtp.example", "MAIL_FROM=demo@example.com", "API_KEY=" + value, "SENTRY_DSN=test-dsn", "FEEDBACK_APP_KEY=test-key"} {
			if !strings.Contains(got, want) {
				t.Fatalf("missing %s contribution", strings.Split(want, "=")[0])
			}
		}
		if strings.Contains(got, "UNRESOLVED=") {
			t.Fatal("unresolved template injected")
		}
	}
}

// A browser is given only what any visitor may read: the error-reporting DSN
// and service-kind values marked browser: true, never an unmarked value.
func TestBrowserEnvironmentGivesOnlyBrowserSafeValues(t *testing.T) {
	t.Parallel()
	s, id, _ := runtimeFixture(t)
	if got, err := s.BrowserEnvironment(context.Background(), id); err != nil || len(got) != 0 {
		t.Fatalf("nothing attached: %v %v", got, err)
	}
	if err := s.Attachments.Remember(t.Context(), id, "errors", "glitchtip", `{"dsn":"https://key@errors.example/3"}`); err != nil {
		t.Fatal(err)
	}
	kind := "capability: mailer\ntitle: Mailer\nenv:\n  - {key: MAILER_PUBLIC_URL, value: \"https://mail.example\", browser: true}\n  - {key: MAILER_RELAY, value: \"smtp://relay.internal\"}\n"
	if err := s.ServiceKinds.Save(context.Background(), "mailer", kind, "Mailer", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.Attachments.Attach(t.Context(), id, "mailer", "relay", `{}`, Entered, "alice", nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.BrowserEnvironment(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"SENTRY_DSN": "https://key@errors.example/3", "MAILER_PUBLIC_URL": "https://mail.example"}
	if len(got) != len(want) {
		t.Fatalf("browser values: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("browser values: %v", got)
		}
	}
	// The container still gets everything.
	if env := strings.Join(mustEnvironment(t, s, id), "\n"); !strings.Contains(env, "MAILER_RELAY=smtp://relay.internal") {
		t.Fatalf("container env: %s", env)
	}
}

// A registry attachment without a credential of its own is written with
// its connector's: username, and the secret tokenFrom names, read from the
// secrets store when used. An attachment's own token still wins; a secret
// the store does not hold is an error, never an anonymous registry.
func TestARegistryConnectorsCredentialComesFromTheSecretsStore(t *testing.T) {
	t.Parallel()
	s, id, dir := runtimeFixture(t)
	if _, err := s.Bindings.Upsert(t.Context(), "registry", CapContainerRegistry, "service", `{"url":"registry.example","group":"team","username":"robot","tokenFrom":["REGISTRY_PASSWORD"]}`, false); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachOfferedRegistry(context.Background(), id, "test", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Registry(context.Background(), id); err == nil || !strings.Contains(err.Error(), "REGISTRY_PASSWORD") {
		t.Fatalf("an unheld credential: %v, want an error naming it", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("REGISTRY_PASSWORD=from-the-store\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Registry(context.Background(), id)
	if err != nil || got == nil || got.Repo != "registry.example/team/demo" || got.Username != "robot" || got.Token != "from-the-store" {
		t.Fatalf("the connector's credential: %+v %v", got, err)
	}
	_, resource, err := s.Attachments.Attached(context.Background(), id, CapContainerRegistry)
	if err != nil || strings.Contains(resource, "from-the-store") || strings.Contains(resource, "token") {
		t.Fatalf("the attachment holds %s %v", resource, err)
	}
	if err := s.Attachments.Attach(t.Context(), id, CapContainerRegistry, "registry", `{"repo":"registry.example/team/demo","username":"own","token":"own-token"}`, Entered, "test", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Registry(context.Background(), id); err != nil || got.Username != "own" || got.Token != "own-token" {
		t.Fatalf("an attachment's own credential: %+v %v", got, err)
	}
}

// failingKinds is a catalog whose definition cannot be read.
type failingKinds struct{}

func (failingKinds) ByCapability(context.Context, string) (*dependency.Kind, error) {
	return nil, errors.New("errors.yaml does not parse")
}

// An attached error-reporting service whose definition cannot be read is an
// error for a static page's values and for a server's environment alike,
// never an empty set.
//
// Kills: the server environment reading the failure as "nothing to give".
func TestAnUnreadableErrorReportingDefinitionIsABrowserError(t *testing.T) {
	t.Parallel()
	s, id, _ := runtimeFixture(t)
	if err := s.Attachments.Attach(t.Context(), id, "errors", "", `{"dsn":"https://k@errors.example/3"}`, Entered, "test", nil); err != nil {
		t.Fatal(err)
	}
	s.kinds = failingKinds{}
	if values, err := s.BrowserEnvironment(context.Background(), id); err == nil || !strings.Contains(err.Error(), "does not parse") {
		t.Fatalf("an unreadable definition is an error for the page: %v %v", values, err)
	}
	if env, err := s.ErrorsEnvironment(context.Background(), id); err == nil || env != nil {
		t.Fatalf("server error-reporting variables: %v %v", env, err)
	}
	if env, err := s.Environment(context.Background(), id); err == nil || env != nil {
		t.Fatalf("server environment: %v %v", env, err)
	}
}

// mustEnvironment is Environment for a test that expects it to succeed.
func mustEnvironment(t *testing.T, s *Service, id ids.ID) []string {
	t.Helper()
	env, err := s.Environment(t.Context(), id)
	if err != nil {
		t.Fatalf("environment: %v", err)
	}
	return env
}

func mustErrorsEnvironment(t *testing.T, s *Service, id ids.ID) []string {
	t.Helper()
	env, err := s.ErrorsEnvironment(t.Context(), id)
	if err != nil {
		t.Fatalf("error-reporting environment: %v", err)
	}
	return env
}

func mustRegistry(t *testing.T, s *Service, id ids.ID) *Registry {
	t.Helper()
	ri, err := s.Registry(t.Context(), id)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return ri
}

// What an app's containers are given is read strictly: an attached
// service whose connector config cannot be read, an error-reporting
// attachment that cannot be read, or a secrets store that cannot be read
// fails the environment, never gives it without those variables.
//
// Kills: each lossy read (connector config as {}, the DSN read as
// unattached, secrets as none).
func TestAnEnvironmentThatCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()
	t.Run("connector config", func(t *testing.T) {
		t.Parallel()
		s, id, _ := runtimeFixture(t)
		if err := s.ServiceKinds.Save(context.Background(), "mailer", "capability: mailer\ntitle: Mail\nenv:\n  - {key: MAIL_HOST, value: \"{{connector.host}}\"}\n", "Mail", "test"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Bindings.Upsert(t.Context(), "smtp", "mailer", "service", `["not","an","object"]`, false); err != nil {
			t.Fatal(err)
		}
		if err := s.Attachments.Attach(t.Context(), id, "mailer", "smtp", `{}`, Entered, "test", nil); err != nil {
			t.Fatal(err)
		}
		if env, err := s.Environment(t.Context(), id); err == nil || env != nil {
			t.Fatalf("an unreadable connector config: %v %v", env, err)
		}
	})
	t.Run("error-reporting attachment", func(t *testing.T) {
		t.Parallel()
		s, id, _ := runtimeFixture(t)
		if err := s.Attachments.Attach(t.Context(), id, "errors", "", `{"dsn":42}`, Entered, "test", nil); err != nil {
			t.Fatal(err)
		}
		if env, err := s.Environment(t.Context(), id); err == nil || env != nil {
			t.Fatalf("an unreadable DSN: %v %v", env, err)
		}
	})
	t.Run("secrets", func(t *testing.T) {
		t.Parallel()
		s, id, dir := runtimeFixture(t)
		if err := s.ServiceKinds.Save(context.Background(), "mailer", "capability: mailer\ntitle: Mail\nenv:\n  - {key: API_KEY, value: \"{{secret:resource.key}}\"}\n", "Mail", "test"); err != nil {
			t.Fatal(err)
		}
		if err := s.Attachments.Attach(t.Context(), id, "mailer", "", `{"key":"MAIL_KEY"}`, Entered, "test", nil); err != nil {
			t.Fatal(err)
		}
		// A folder where the secrets file belongs: it cannot be read.
		if err := os.Mkdir(filepath.Join(dir, "secrets.env"), 0o700); err != nil {
			t.Fatal(err)
		}
		if env, err := s.Environment(t.Context(), id); err == nil || env != nil {
			t.Fatalf("unreadable secrets: %v %v", env, err)
		}
	})
}

// A registry binding whose config cannot be read is an error, never left
// out: a mirror left out sends builds straight to the upstream registry.
//
// Kills: the undecodable binding skipped.
func TestAnUndecodableRegistryBindingIsAnError(t *testing.T) {
	t.Parallel()
	s, _, _ := runtimeFixture(t)
	if _, err := s.Bindings.Upsert(t.Context(), "npm-mirror", CapRegistry, "direct", `{"format":"npm","url":"https://npm.example"}`, false); err != nil {
		t.Fatal(err)
	}
	if got, err := RegistryBindings(t.Context(), s.Bindings.db); err != nil || got["npm"].URL != "https://npm.example" {
		t.Fatalf("a readable binding: %v %v", got, err)
	}
	if _, err := s.Bindings.Upsert(t.Context(), "go-mirror", CapRegistry, "direct", `{"format":7}`, false); err != nil {
		t.Fatal(err)
	}
	if got, err := RegistryBindings(t.Context(), s.Bindings.db); err == nil || got != nil {
		t.Fatalf("an undecodable binding: %v %v", got, err)
	}
}

// A saved connector may rotate credentials, but may not redirect an existing
// attachment's credential or send a replacement connector's credential to it.
func TestRegistryResolutionRefusesAChangedConnectorDestination(t *testing.T) {
	t.Parallel()
	for _, ownToken := range []bool{false, true} {
		t.Run(fmt.Sprint(ownToken), func(t *testing.T) {
			s, id, dir := runtimeFixture(t)
			if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("REGISTRY_A=token-a\nREGISTRY_B=token-b\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Bindings.Save(t.Context(), "registry", CapContainerRegistry, "service", `{"url":"a.registry.example","group":"team","username":"robot-a","tokenFrom":"REGISTRY_A"}`, "fixture"); err != nil {
				t.Fatal(err)
			}
			if err := s.AttachOfferedRegistry(t.Context(), id, "fixture", nil); err != nil {
				t.Fatal(err)
			}
			if ownToken {
				if err := s.Attachments.Attach(t.Context(), id, CapContainerRegistry, "registry", `{"repo":"a.registry.example/team/demo","username":"own-a","token":"own-token-a"}`, Entered, "fixture", nil); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := s.Registry(t.Context(), id); err != nil || got == nil || got.Host != "a.registry.example" {
				t.Fatalf("original attachment: %+v %v", got, err)
			}
			if _, err := s.Bindings.Save(t.Context(), "registry", CapContainerRegistry, "service", `{"url":"b.registry.example","group":"team","username":"robot-b","tokenFrom":"REGISTRY_B"}`, "fixture"); err != nil {
				t.Fatal(err)
			}
			got, err := s.Registry(t.Context(), id)
			if err == nil || got != nil {
				t.Fatalf("mixed registry destination admitted: result=%+v err=%v", got, err)
			}
		})
	}
}

func TestRegistryCredentialsRotateOnlyWithinAnExplicitConnectorDestination(t *testing.T) {
	t.Parallel()
	s, id, dir := runtimeFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("REGISTRY_A=token-a\nREGISTRY_B=token-b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for i, key := range []string{"REGISTRY_A", "REGISTRY_B"} {
		cfg := fmt.Sprintf(`{"url":"registry.example:5000","username":"robot","tokenFrom":%q}`, key)
		if _, err := s.Bindings.Save(t.Context(), "registry", CapContainerRegistry, "service", cfg, "fixture"); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err := s.Attachments.Attach(t.Context(), id, CapContainerRegistry, "registry", `{"repo":"registry.example:5000/team/demo"}`, Entered, "fixture", nil); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.Registry(t.Context(), id)
		if err != nil || got == nil || got.Token != []string{"token-a", "token-b"}[i] || got.Host != "registry.example:5000" {
			t.Fatalf("same-destination rotation: %+v %v", got, err)
		}
	}
	if _, err := s.Bindings.Save(t.Context(), "registry", CapContainerRegistry, "service", `{"username":"robot","tokenFrom":"REGISTRY_B"}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Registry(t.Context(), id); err == nil || got != nil {
		t.Fatalf("hostless connector credential admitted: %+v %v", got, err)
	}
	if err := s.Attachments.Attach(t.Context(), id, CapContainerRegistry, "registry", `{"repo":"registry.example:5000/team/demo","username":"own","token":"own-token"}`, Entered, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Registry(t.Context(), id); err != nil || got == nil || got.Token != "own-token" {
		t.Fatalf("self-contained attachment: %+v %v", got, err)
	}
	if err := s.Attachments.Attach(t.Context(), id, CapContainerRegistry, "", `{"repo":"team/demo"}`, Entered, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Registry(t.Context(), id); err != nil || got == nil || got.Host != "index.docker.io" || got.Token != "" {
		t.Fatalf("anonymous implicit repository: %+v %v", got, err)
	}
}

func TestRegistryDestinationIsRefusedBeforeReadingAConnectorSecret(t *testing.T) {
	t.Parallel()
	s, id, dir := runtimeFixture(t)
	if _, err := s.Bindings.Save(t.Context(), "registry", CapContainerRegistry, "service", `{"url":"b.registry.example","tokenFrom":"PASSWORD"}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := s.Attachments.Attach(t.Context(), id, CapContainerRegistry, "registry", `{"repo":"a.registry.example/team/demo"}`, Entered, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "secrets.env"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := s.Registry(t.Context(), id)
	if got != nil || err == nil || !strings.Contains(err.Error(), "different registries") {
		t.Fatalf("destination must refuse before secret read: %+v %v", got, err)
	}
}
