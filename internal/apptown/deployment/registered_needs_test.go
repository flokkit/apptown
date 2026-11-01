package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/openrouter/openrouterfake"
	"github.com/flokkit/apptown/stock"
)

const llmProvisioningSentinel = "sk-or-v1-PROVISIONING-SENTINEL-d41c"

const llmAppFixture = `app:
  web:
    image: reg.example/app:1
    ports: ["8080"]
    public: true
needs:
  llm: {limit: 3, limitReset: monthly}
`

// withLLM gives s an AppTown that holds an OpenRouter provisioning key, an AI
// models connector on a fake OpenRouter, and the stock dependency kinds;
// attached says whether the project attached AI models.
func withLLM(t *testing.T, s *Service, projectID ids.ID, attached bool) *openrouterfake.Server {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("OPENROUTER_PROVISIONING_KEY="+llmProvisioningSentinel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secrets := secret.NewService(s.db, secret.Config{DataDir: dir})
	s.env.secrets = secrets
	if s.env.needs == nil {
		s.env.needs = dependency.NewCatalog(s.db, nil)
		if _, _, err := s.env.needs.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
			t.Fatal(err)
		}
	}
	s.env.integrations = integration.NewService(s.db, integration.Dependencies{Kinds: s.env.needs, Projects: project.New(s.db, nil), Secrets: secrets})
	f := openrouterfake.New(llmProvisioningSentinel)
	t.Cleanup(f.Close)
	if _, err := s.env.integrations.Bindings.Save(context.Background(), "openrouter", integration.CapLLM, "openrouter", `{"url":"`+f.Base+`","provisioningKeyFrom":["OPENROUTER_PROVISIONING_KEY"],"maxLimit":5,"previewLimit":1}`, "admin"); err != nil {
		t.Fatal(err)
	}
	if attached {
		if err := s.env.integrations.Attachments.Attach(t.Context(), projectID, integration.CapLLM, "openrouter", "{}", "owner", nil); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// llmWorld is a Docker-host world with withLLM.
func llmWorld(t *testing.T, attached bool) (*Service, *database.DB, *dockerfake.Daemon, ids.ID, *openrouterfake.Server) {
	t.Helper()
	s, db, client, _, projectID := hostWorld(t, "")
	return s, db, client, projectID, withLLM(t, s, projectID, attached)
}

func llmContract(t *testing.T, yaml string) (*appcontract.Contract, string) {
	t.Helper()
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := docker.ReadWorkspaceContract(ws)
	if err != nil {
		t.Fatal(err)
	}
	return c, ws
}

func llmDeployment(t *testing.T, s *Service, db *database.DB, projectID ids.ID, name string) *Deployment {
	t.Helper()
	target, err := s.Targets.ByName(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	id := storedDeploymentFixture(t, db, deploymentsql.EnsureStandingParams{ID: ids.New(time.Now()), ProjectID: projectID, Name: name, TargetID: target.ID})
	dep, err := s.Deployments.ByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return dep
}

func bootDeployment(s *Service, client *dockerfake.Daemon, dep *Deployment, c *appcontract.Contract, ws string) (docker.BootResult, error) {
	return s.env.Host(dep.ProjectID, s.env.NeedCredentials(NeedSecretsScope(dep.ID)), func(slug string) KeyOwner { return deploymentKeys(s.db, s.clock, dep, slug) }).Boot(
		context.Background(), docker.BootRequest{Allowed: docker.AuthorisedWhenRequested, Docker: docker.Scope{Client: client}, Name: "forge-p-" + dep.Name, StackID: "p-" + dep.Name, Workspace: ws, Slug: "p", Deployment: dep.Name, Probe: time.Second}, c)
}

func runCalls(client *dockerfake.Daemon) string {
	var out []string
	for _, args := range client.Matching("run ") {
		out = append(out, strings.Join(args, " "))
	}
	return strings.Join(out, "\n")
}

// A deployment of an app that needs AI models gets its own key with the
// declared budget, injected as the two variables, owned by the deployment;
// booting it again keeps the same key. The provisioning key is in no
// container.
func TestADeploymentGetsItsOwnKeyAndKeepsIt(t *testing.T) {
	t.Parallel()
	s, db, client, projectID, f := llmWorld(t, true)
	dep := llmDeployment(t, s, db, projectID, "prod")
	c, ws := llmContract(t, llmAppFixture)
	res, err := bootDeployment(s, client, dep, c, ws)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := s.Deployments.Owned(context.Background(), dep.ID)
	if err != nil || len(owned) != 1 || owned[0].Kind != KindOpenRouterKey || owned[0].Need != "llm" {
		t.Fatalf("the deployment owns its key: %+v %v", owned, err)
	}
	held, _ := integration.ParseHeldKey(owned[0].ID)
	key := f.Keys()[held.Hash]
	if key.Limit == nil || *key.Limit != 3 || key.Reset == nil || *key.Reset != "monthly" || key.Name != "apptown-p-prod-"+itoa(dep.ID) {
		t.Fatalf("key at OpenRouter: %+v", key)
	}
	runs := runCalls(client)
	if !strings.Contains(runs, "LLM_API_KEY="+key.Value) || !strings.Contains(runs, "LLM_BASE_URL="+f.Base) {
		t.Fatalf("the app is given the key and base URL:\n%s", runs)
	}
	if strings.Contains(runs, llmProvisioningSentinel) {
		t.Fatal("the provisioning key reached a container")
	}
	var named bool
	for _, r := range res.Stack.Secrets.Receipts {
		named = named || r.Name == "LLM_API_KEY"
		if strings.Contains(r.Version, key.Value) {
			t.Fatal("a receipt carries the key")
		}
	}
	if !named {
		t.Fatalf("the boot's receipts name the key: %+v", res.Stack.Secrets.Receipts)
	}
	if _, err := bootDeployment(s, client, dep, c, ws); err != nil {
		t.Fatal(err)
	}
	if f.CallsOf("POST") != 1 || f.CallsOf("PATCH") != 1 {
		t.Fatalf("a second boot keeps the key and sets its budget: %d POST, %d PATCH", f.CallsOf("POST"), f.CallsOf("PATCH"))
	}
}

// Declaring the need without the project's attachment boots nothing and
// calls OpenRouter not at all.
func TestADeploymentWithoutTheAttachmentGetsNoKeyAndDoesNotBoot(t *testing.T) {
	t.Parallel()
	s, db, client, projectID, f := llmWorld(t, false)
	dep := llmDeployment(t, s, db, projectID, "prod")
	c, ws := llmContract(t, llmAppFixture)
	if _, err := bootDeployment(s, client, dep, c, ws); !errors.Is(err, integration.ErrLLMNotAttached) {
		t.Fatalf("want not attached, got %v", err)
	}
	if n := len(f.Calls()); n != 0 {
		t.Fatalf("%d OpenRouter calls without the attachment", n)
	}
	if runs := runCalls(client); runs != "" {
		t.Fatalf("something ran:\n%s", runs)
	}
	if owned, err := s.Deployments.Owned(context.Background(), dep.ID); err != nil || len(owned) != 0 {
		t.Fatalf("owned: %+v %v", owned, err)
	}
}

// Error reporting is an integration: its DSN arrives from the attachment,
// with no need declared, alone or beside an AI models key, and an app with
// no AI models need makes no OpenRouter call. Declaring it under needs: is
// refused (dependency.RefusesNeeds). A register adapter AppTown does not
// run refuses.
func TestAttachedErrorReportingStillArrivesBesideAKey(t *testing.T) {
	t.Parallel()
	s, db, client, projectID, f := llmWorld(t, true)
	if err := s.env.integrations.Attachments.Attach(t.Context(), projectID, integration.CapErrors, "", `{"dsn":"https://pub@errors.test/7"}`, "owner", nil); err != nil {
		t.Fatal(err)
	}
	only, ws := llmContract(t, "app:\n  web:\n    image: reg.example/app:1\n    ports: [\"8080\"]\n    public: true\n")
	if _, err := bootDeployment(s, client, llmDeployment(t, s, db, projectID, "errs"), only, ws); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runCalls(client), "SENTRY_DSN=https://pub@errors.test/7") || len(f.Calls()) != 0 {
		t.Fatalf("errors alone: %d OpenRouter calls\n%s", len(f.Calls()), runCalls(client))
	}
	both, ws := llmContract(t, "app:\n  web:\n    image: reg.example/app:1\n    ports: [\"8080\"]\n    public: true\nneeds:\n  llm: {}\n")
	if _, err := bootDeployment(s, client, llmDeployment(t, s, db, projectID, "both"), both, ws); err != nil {
		t.Fatal(err)
	}
	runs := runCalls(client)
	if !strings.Contains(runs, "--name forge-p-both") || strings.Count(runs, "SENTRY_DSN=https://pub@errors.test/7") < 2 || !strings.Contains(runs, "LLM_API_KEY=") || f.CallsOf("POST") != 1 {
		t.Fatalf("errors and llm:\n%s", runs)
	}
	if _, err := s.env.needs.Save(context.Background(), "capability: mystery\ntitle: Mystery\nregister:\n  connector: x\n  adapter: mystery-adapter\n", "admin"); err != nil {
		t.Fatal(err)
	}
	odd, ws := llmContract(t, "app:\n  web:\n    image: reg.example/app:1\n    ports: [\"8080\"]\n    public: true\nneeds:\n  mystery: {}\n")
	if _, err := bootDeployment(s, client, llmDeployment(t, s, db, projectID, "odd"), odd, ws); err == nil || !strings.Contains(err.Error(), "mystery-adapter") {
		t.Fatalf("an adapter AppTown does not run: %v", err)
	}
}

// A target is asked only for what it provides itself: a managed target is
// not asked for AI models, and the release's values carry them to it. A
// static-only bucket, with no server to hold a key, still refuses.
func TestTargetsAreNotAskedForRegisteredNeeds(t *testing.T) {
	t.Parallel()
	s, _, _, _, _ := llmWorld(t, true)
	c, _ := llmContract(t, "app:\n  web:\n    image: reg.example/app:1\n    ports: [\"8080\"]\n    public: true\nneeds:\n  llm: {}\n  postgres: {}\n")
	managed := &Target{Name: "cloud", Kind: KindDOApp, NeedsMode: "managed", Capabilities: TargetCapabilities{Needs: []string{"postgres"}}}
	if managed.RefusesContract(c) == "" {
		t.Fatal("fixture: the target itself cannot provide llm")
	}
	if why, err := s.env.RefusesContract(t.Context(), managed, c); err != nil || why != "" {
		t.Fatalf("a managed target is not asked for llm: %q %v", why, err)
	}
	if why, _ := s.env.RefusesContract(t.Context(), &Target{Name: "bucket", Kind: KindS3Bucket}, c); why == "" {
		t.Fatal("a bucket refuses an app with needs")
	}
	given := registeredSecrets{inner: nil, env: map[string]string{"LLM_API_KEY": "k"}}
	if given.EnvFor("web")["LLM_API_KEY"] != "k" {
		t.Fatal("the release's values carry the key")
	}
}

// needDriver is a managed target that says which needs it was asked for.
type needDriver struct {
	fakeDriver
	asked []string
}

func (d *needDriver) FulfilNeed(_ context.Context, _ deploy.Placement, need string, _ map[string]string, _ func(string, ...any)) (deploy.NeedBinding, map[string]any, error) {
	d.asked = append(d.asked, need)
	return deploy.NeedBinding{Env: map[string]string{"DATABASE_URL": "postgres://db"}}, map[string]any{"id": "db-1"}, nil
}

// A managed target's need module is asked for what it provides, never for
// a need fulfilled by registering.
func TestManagedTargetsFulfilOnlyTheirOwnNeeds(t *testing.T) {
	t.Parallel()
	s, _, _, dep := removalFixture(t)
	withLLM(t, s, dep.ProjectID, true)
	target, err := s.Targets.ByID(t.Context(), dep.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := llmContract(t, "app:\n  web:\n    image: reg.example/app:1\n    ports: [\"8080\"]\n    public: true\nneeds:\n  llm: {}\n  postgres: {}\n")
	d := &needDriver{}
	bindings, err := s.providers.PrepareNeeds(context.Background(), d, target, dep, c, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(d.asked, ",") != "postgres" || len(bindings) != 1 {
		t.Fatalf("the target was asked for %v", d.asked)
	}
}

func itoa(n ids.ID) string { return n.String() }

// A deployment on a cloud target gets its key with the app's secrets: the
// release's values carry it to every part, the deployment owns it, and the
// target's need module is asked only for its own needs.
func TestACloudDeploymentIsGivenItsKeyWithTheAppsSecrets(t *testing.T) {
	t.Parallel()
	service, provider, _, pid, dep, bundle := doReleaseWorldNeeding(t, "  llm:\n    limit: 4\n")
	f := withLLM(t, service, pid, true)
	accepted := doAcceptRelease(t, service, pid, dep, bundle)
	if err := service.ExecuteRelease(context.Background(), accepted.ID); err != nil {
		t.Fatal(err)
	}
	owned, err := service.Deployments.Owned(context.Background(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	held := heldKeys(owned)
	if len(held) != 1 || f.CallsOf("POST") != 1 {
		t.Fatalf("the deployment owns one key: %+v", owned)
	}
	key := f.Keys()[held[0].Hash]
	if *key.Limit != 4 {
		t.Fatalf("the declared limit: %v", *key.Limit)
	}
	state := provider.Snapshot()
	spec := fmt.Sprint(state.Specs)
	if !strings.Contains(spec, "LLM_API_KEY") || !strings.Contains(spec, key.Value) || !strings.Contains(spec, "LLM_BASE_URL") {
		t.Fatalf("the app spec carries the key: %s", spec)
	}
	if strings.Contains(spec, llmProvisioningSentinel) {
		t.Fatal("the provisioning key reached the provider")
	}
	if len(state.Databases) != 1 {
		t.Fatalf("postgres alone is the target's: %d databases", len(state.Databases))
	}
}

// The deployment says which keys it owns, and what OpenRouter says of each
// now: the allowance over the key's own period, lifetime spend apart, and
// unknown (never zero) when OpenRouter cannot be asked.
func TestADeploymentShowsItsKeysAllowance(t *testing.T) {
	t.Parallel()
	s, db, client, projectID, f := llmWorld(t, true)
	s.authority = &configurationWorld{actor: configurationIdentity{Name: "alice"}}
	dep := llmDeployment(t, s, db, projectID, "prod")
	c, ws := llmContract(t, llmAppFixture)
	if _, err := bootDeployment(s, client, dep, c, ws); err != nil {
		t.Fatal(err)
	}
	summaries, err := s.List(context.Background(), projectID)
	if err != nil || len(summaries) != 1 || len(summaries[0].AIModelsKeys) != 1 {
		t.Fatalf("the listing names the key: %+v %v", summaries, err)
	}
	held := heldKeys(mustOwned(t, s, dep.ID))
	f.SetUsage(held[0].Hash, 12, 1)
	keys, err := s.AIModels(context.Background(), projectID, "prod")
	if err != nil || len(keys) != 1 || keys[0].Allowance == nil {
		t.Fatalf("allowance: %+v %v", keys, err)
	}
	a := keys[0].Allowance
	if *a.Remaining != 2 || a.Period != 1 || a.Lifetime != 12 || a.Reset != "monthly" {
		t.Fatalf("allowance over its own period: %+v", a)
	}
	f.FailNext("GET", 503)
	keys, err = s.AIModels(context.Background(), projectID, "prod")
	if err != nil || keys[0].Allowance != nil || !strings.Contains(keys[0].Unknown, "503") {
		t.Fatalf("unknown is said: %+v %v", keys, err)
	}
	if strings.Contains(keys[0].Unknown, llmProvisioningSentinel) {
		t.Fatal("the reason carries the provisioning key")
	}
}

func mustOwned(t *testing.T, s *Service, id ids.ID) []deploy.Owned {
	t.Helper()
	owned, err := s.Deployments.Owned(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return owned
}

// This checked port changes permission at an observable external effect,
// rather than depending on how many internal rechecks execute.
type registeredReleaseAuthority struct {
	denied func() bool
}

func (a registeredReleaseAuthority) RequireStill(ctx context.Context, principal access.Principal, permission access.Permission) error {
	if err := (&requestAuthority{}).RequireStill(ctx, principal, permission); err != nil {
		return err
	}
	if a.denied() {
		return errors.New("release permission withdrawn")
	}
	return nil
}

// Withdrawing authority after key issuance must not lose its owner, create
// managed needs, or deliver the key to an application. A newly authorized
// request reuses that exact key, proving the failed attempt left recoverable
// ownership rather than an untracked external credential.
func TestAReleaseDeniedAfterKeyIssuanceKeepsItsKeyOwned(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, phase    string
		defaultRelease bool
	}{
		{"named/before key", "before key", false}, {"named/after key", "after key", false},
		{"default/before key", "before key", true}, {"default/after key", "after key", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			phase := tc.phase
			t.Parallel()
			service, provider, _, pid, dep, bundle := doReleaseWorldNeeding(t, "  llm:\n    limit: 4\n")
			f := withLLM(t, service, pid, true)
			admit := func() AcceptedRelease { return doAcceptRelease(t, service, pid, dep, bundle) }
			if tc.defaultRelease {
				service.configuration.Authority = service.release.Authority
				service.configuration.Projects = service.release.Sources
				target, err := service.Targets.ByID(t.Context(), dep.TargetID)
				if err != nil {
					t.Fatal(err)
				}
				if err := service.Targets.Offer(t.Context(), target, "*", OfferCaps{}, "fixture"); err != nil {
					t.Fatal(err)
				}
				selected, err := service.SetReleaseDefault(t.Context(), pid, dep.ID, 0)
				if err != nil {
					t.Fatal(err)
				}
				admit = func() AcceptedRelease {
					t.Helper()
					accepted, err := service.RequestDefaultRelease(t.Context(), DefaultReleaseSelection{ProjectID: pid, ExpectedRevision: &selected.Revision})
					if err != nil {
						t.Fatal(err)
					}
					return accepted
				}
			}
			service.release.Still = registeredReleaseAuthority{denied: func() bool { return phase == "before key" || f.CallsOf("POST") > 0 }}
			accepted := admit()
			if err := service.ExecuteRelease(t.Context(), accepted.ID); err == nil {
				t.Fatal("withdrawn release permission still executed")
			}
			rel, err := service.Releases.ByNumber(t.Context(), pid, accepted.Number)
			if err != nil || rel.State != ReleaseFailed {
				t.Fatalf("failed release: %+v %v", rel, err)
			}
			state := provider.Snapshot()
			if len(state.Databases) != 0 || len(state.Apps) != 0 {
				t.Fatal("provider effects followed withdrawal")
			}
			held := heldKeys(mustOwned(t, service, dep.ID))
			if phase == "before key" {
				if len(held) != 0 || f.CallsOf("POST") != 0 {
					t.Fatal("key issued before permission")
				}
			} else {
				if len(held) != 1 || f.CallsOf("POST") != 1 {
					t.Fatalf("issued key lost its owner: %+v", held)
				}
				if _, ok := f.Keys()[held[0].Hash]; !ok {
					t.Fatal("owned key does not identify issued key")
				}
			}
			service.release.Still = &requestAuthority{}
			retried := admit()
			if err := service.ExecuteRelease(t.Context(), retried.ID); err != nil {
				t.Fatal(err)
			}
			rel, err = service.Releases.ByNumber(t.Context(), pid, retried.Number)
			if err != nil || rel.State != Released {
				t.Fatalf("authorized retry: %+v %v", rel, err)
			}
			recovered := heldKeys(mustOwned(t, service, dep.ID))
			if len(recovered) != 1 || f.CallsOf("POST") != 1 {
				t.Fatalf("retry leaked a second key: %+v", recovered)
			}
			if phase == "after key" && recovered[0].Hash != held[0].Hash {
				t.Fatal("retry replaced the retained key")
			}
		})
	}
}

func TestRegisteredNeedRefusesOriginalScopedDeliveryBeforeRegistration(t *testing.T) {
	s, db, client, pid, f := llmWorld(t, true)
	dep := llmDeployment(t, s, db, pid, "prod")
	c, ws := llmContract(t, llmAppFixture)
	c.Parts = append(c.Parts, appcontract.Part{Name: "worker", Image: "app:1"})
	c.Init = append(c.Init, appcontract.InitContainer{Name: "migrate", Image: "app:1"})
	owner := deploymentKeys(s.db, s.clock, dep, "p")
	for _, consumer := range []string{"web", "worker", "init-migrate"} {
		for _, name := range []string{"LLM_API_KEY", "LLM_BASE_URL"} {
			for _, value := range []string{"manual-sentinel", "", f.Base} {
				p := docker.Prepared{Contract: c, Secrets: map[string]map[string]string{consumer: {name: value}}}
				before, _ := json.Marshal(p)
				got, err := s.env.WithRegistered(t.Context(), p, owner)
				after, _ := json.Marshal(got)
				if err == nil || !strings.Contains(err.Error(), consumer) || !strings.Contains(err.Error(), name) || strings.Contains(err.Error(), "sentinel") || string(before) != string(after) {
					t.Fatalf("original %s/%s lost or accepted: %v", consumer, name, err)
				}
			}
		}
	}
	if f.CallsOf("POST") != 0 {
		t.Fatal("ambiguous original delivery reached registration")
	}
	// Exercise the actual local secret resolver and boot, not only the
	// prepared-value path. The configured secret exists and is in web scope.
	if err := os.WriteFile(filepath.Join(ws, secret.SpecFile), []byte("[project]\nname='p'\nrevision='1.0'\n[profiles.default]\nLLM_API_KEY={description='manual'}\n[scopes.web]\nsecrets=['LLM_API_KEY']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := s.env.secrets.Vault.Put(t.Context(), "collision", "LLM_API_KEY", "manual-sentinel", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.env.secrets.Bindings.Put(t.Context(), pid, secret.Binding{Deployment: dep.Name, Name: "LLM_API_KEY", Store: secret.StoreInternal, Ref: "collision", Version: fmt.Sprint(v), UpdatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := bootDeployment(s, client, dep, c, ws); err == nil || !strings.Contains(err.Error(), "LLM_API_KEY") || strings.Contains(err.Error(), "manual-sentinel") {
		t.Fatalf("local boot accepted ambiguous registration: %v", err)
	}
	if f.CallsOf("POST") != 0 || len(client.Matching("run ")) != 0 {
		t.Fatal("ambiguous local boot reached provider or app")
	}
	// Ordinary noncolliding preparation still delivers both authorities.
	p, err := s.env.WithRegistered(t.Context(), docker.Prepared{Contract: c, Secrets: map[string]map[string]string{"web": {"OTHER_KEY": "kept"}}}, owner)
	if err != nil || p.Secrets["web"]["OTHER_KEY"] != "kept" || p.Secrets["web"]["LLM_API_KEY"] == "" || f.CallsOf("POST") != 1 {
		t.Fatalf("noncolliding registration: %v", err)
	}
}
