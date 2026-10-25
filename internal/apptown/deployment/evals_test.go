package deployment

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/oci"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
)

// A copy goes only on an eval target: this AppTown's own host when it has an
// eval domain, or a Docker host attached for evals; never an ordinary
// attached host or a provider's platform. Nothing else goes on a host
// attached for evals. A copy's landing creates its deployment on the eval
// target, and an ordinary landing never picks a host attached for evals.
func TestEvalCopiesArePlacedOnEvalTargetsOnly(t *testing.T) {
	t.Parallel()
	w := noLocalDockerWorld(t, multipartApp)
	ctx := context.Background()
	real := w.project
	copy := createTestCopy(t, w.db, "game-copy", "Game copy")
	s := w.service(false)
	builtIn, err := s.Targets.Put(ctx, &Target{Name: "local", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	evalHost, err := s.Targets.Put(ctx, &Target{Name: "evals", Kind: KindDockerHost, Origin: OriginAttached, Status: TargetReady,
		Address: map[string]any{"host": "ssh://deploy@127.0.0.3", "keyFrom": "REMOTE_KEY"}, Ingress: map[string]any{"domain": "eval.example.com", IngressEvals: true}})
	if err != nil {
		t.Fatal(err)
	}
	platform := &Target{Name: "cloud", Kind: KindDOApp, Origin: OriginAttached, Status: TargetReady}
	for _, c := range []struct {
		name    string
		project ids.ID
		target  *Target
		allowed bool
	}{
		{"a copy on the built-in host with no eval domain", copy, builtIn, false},
		{"a copy on an ordinary attached host", copy, w.remote, false},
		{"a copy on a provider's platform", copy, platform, false},
		{"a copy on a host attached for evals", copy, evalHost, true},
		{"a real project on a host attached for evals", real, evalHost, false},
		{"a real project on the built-in host", real, builtIn, true},
		{"a real project on an ordinary attached host", real, w.remote, true},
	} {
		err := s.refusesPlacement(ctx, c.project, c.target)
		if c.allowed != (err == nil) || (!c.allowed && !errors.Is(err, ErrReleaseRefused)) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// A project Project cannot read is placed nowhere, not as an ordinary one.
	if err := s.refusesPlacement(ctx, idstest.N(999999), builtIn); err == nil {
		t.Fatal("a project whose purpose cannot be read was placed")
	}
	if err := s.RefusesLanding(ctx, copy, w.remote); !errors.Is(err, ErrReleaseRefused) {
		t.Fatalf("a copy's landing on an ordinary host: %v", err)
	}
	s.env.routes.EvalDomain = "eval.example.com"
	if err := s.refusesPlacement(ctx, copy, builtIn); err != nil {
		t.Fatalf("a copy on the built-in host with an eval domain: %v", err)
	}

	attrs := Attributes{Feed: "landing", Audience: "team", DataClass: "synthetic"}
	if got, err := s.LandingTarget(ctx, copy, "main", attrs); err != nil || got.ID != evalHost.ID {
		t.Fatalf("a copy lands on the host attached for evals: %+v %v", got, err)
	}
	mode := w.service(true)
	for _, target := range []*Target{w.remote, evalHost} {
		if err := mode.Targets.Offer(ctx, target, "*", OfferCaps{}, "boss"); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := mode.LandingTarget(ctx, real, "main", attrs); err != nil || got.ID != w.remote.ID {
		t.Fatalf("an ordinary landing never picks the host attached for evals: %+v %v", got, err)
	}
}

// A copy's addresses are named on the eval domain, every other project's
// on its own domains (Preview refuses a copy's preview with no eval domain).
func TestACopysAddressesAreOnTheEvalDomain(t *testing.T) {
	t.Parallel()
	w := noLocalDockerWorld(t, multipartApp)
	copy := createTestCopy(t, w.db, "game-copy", "Game copy")
	s := w.service(false)
	s.env.routes.EvalDomain, s.env.routes.EvalScheme = "eval.example.com", "https"
	s.env.routes.PreviewDomain, s.env.routes.PreviewScheme = "preview.example.com", "https"
	if u := s.env.RoutesFor(t.Context(), copy).PreviewURL("change-3", 0); u != "https://change-3.eval.example.com" {
		t.Fatalf("a copy's preview address: %s", u)
	}
	if u := s.env.RoutesFor(t.Context(), w.project).PreviewURL("change-4", 0); u != "https://change-4.preview.example.com" {
		t.Fatalf("a real project's preview address: %s", u)
	}
}

// A person keeps an attached Docker host for evals by editing it: only a
// Docker host with a domain, which is then the eval domain; the receipt
// carries before and after, and clearing the mark returns it to ordinary
// work.
func TestAnAttachedDockerHostIsKeptForEvalsByAnEdit(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	targets := NewTargets(db, nil)
	ctx := context.Background()
	host, err := targets.Put(ctx, &Target{Name: "host", Kind: KindDockerHost, Origin: OriginAttached, Status: TargetReady, Address: map[string]any{"host": "ssh://deploy@h"}})
	if err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	rev := host.SettingsRevision
	if _, err := targets.EditSettings(ctx, "host", TargetSettingsEdit{ExpectedRevision: &rev, Evals: &yes}, "alice"); !errors.Is(err, ErrTargetEvals) {
		t.Fatalf("no domain, no eval domain: %v", err)
	}
	domain := "eval.example.com"
	kept, err := targets.EditSettings(ctx, "host", TargetSettingsEdit{ExpectedRevision: &rev, Domain: &domain, Evals: &yes}, "alice")
	if err != nil || !kept.ForEvals() {
		t.Fatalf("kept for evals: %+v %v", kept, err)
	}
	rev = kept.SettingsRevision
	back, err := targets.EditSettings(ctx, "host", TargetSettingsEdit{ExpectedRevision: &rev, Evals: &no}, "alice")
	if err != nil || back.ForEvals() {
		t.Fatalf("returned to ordinary work: %+v %v", back, err)
	}
}

// A copy's landing with the usual offered registry attaches nothing: it
// publishes under the copy's own repository with AppTown's credential.
func TestACopysLandingPublishesUnderItsOwnRepositoryAndAttachesNothing(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	dir := t.TempDir()
	copy := createTestCopy(t, db, "shop-copy", "Shop copy")
	projects := project.New(db, nil)
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects, Secrets: secret.NewService(db, secret.Config{DataDir: dir})})
	if _, err := integrations.Bindings.Upsert(t.Context(), "registry", integration.CapContainerRegistry, "service", `{"url":"registry.example","group":"team"}`, false); err != nil {
		t.Fatal(err)
	}
	s := New(context.Background(), db, assembled(db, testRuntime{DataDir: dir, Projects: projects, Integrations: integrations}, Dependencies{}))
	repo, err := s.LandingRegistry(context.Background(), copy, "apptown-system", nil)
	if err != nil || repo.Name != "registry.example/team/shop-copy" {
		t.Fatalf("the copy's repository: %+v %v", repo, err)
	}
	if attached, err := integrations.Attachments.IsAttached(t.Context(), copy, integration.CapContainerRegistry); err != nil || attached {
		t.Fatalf("a copy's landing attached a registry: %v %v", attached, err)
	}
}

// AppTown's credential for a copy's own repository reaches that
// repository only: an image the copy's code names is read with no
// credential as it is published, however it is named, while an ordinary
// project's own-registry image is read with its credential.
func TestACopysCodeNamesNoImageAppTownsCredentialReads(t *testing.T) {
	t.Parallel()
	repo := oci.Repository{Name: "registry.example/evals/shop-copy", Credential: oci.Credential{Username: "robot", Password: "forge-held"}}
	for _, source := range []string{"registry.example/team/real-payroll:latest", "registry.example/evals/shop-copy:1"} {
		if got := sourceCredential(true, repo, source); got != (oci.Credential{}) {
			t.Fatalf("a copy reads %s with %+v", source, got)
		}
	}
	if got := sourceCredential(false, repo, "registry.example/team/real:1"); got.Password != "forge-held" {
		t.Fatalf("an ordinary project's own image: %+v", got)
	}
}

// A copy's published images are pulled with its own repository's
// credential only for a bundle of that repository; any other bundle gets
// nothing of it.
func TestACopysPullCredentialIsForItsOwnBundleOnly(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	dir := t.TempDir()
	copy := createTestCopy(t, db, "shop-copy", "Shop copy")
	projects := project.New(db, nil)
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("REGISTRY_PASSWORD=forge-held\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects, Secrets: secret.NewService(db, secret.Config{DataDir: dir})})
	if _, err := integrations.Bindings.Upsert(t.Context(), "registry", integration.CapContainerRegistry, "service", `{"url":"registry.example","group":"evals","username":"robot","tokenFrom":["REGISTRY_PASSWORD"]}`, false); err != nil {
		t.Fatal(err)
	}
	s := New(context.Background(), db, assembled(db, testRuntime{DataDir: dir, Projects: projects, Integrations: integrations}, Dependencies{}))
	own := &ReleaseBundle{Parts: map[string]string{"web": "registry.example/evals/shop-copy@sha256:" + strings.Repeat("a", 64)}, BuiltFrom: BundleOrigin{Registry: "registry.example/evals/shop-copy", Platform: "linux/amd64"}}
	if got, err := s.env.BundleLogin(t.Context(), copy, own); err != nil || got == nil || got.Token != "forge-held" || got.Host != "registry.example" {
		t.Fatalf("the copy's own bundle: %+v %v", got, err)
	}
	p := &Providers{env: s.env}
	for ref, allowed := range map[string]bool{"registry.example/evals/shop-copy:latest": true, "registry.example/team/real:latest": false, "elsewhere.example/evals/shop-copy:latest": false} {
		got, err := p.releaseCredential(t.Context(), copy, ref)
		if err != nil || (got.Password == "forge-held") != allowed {
			t.Fatalf("copy source %s: selected=%v err=%v", ref, got.Password != "", err)
		}
	}
	other := &ReleaseBundle{Parts: map[string]string{"web": "registry.example/team/real@sha256:" + strings.Repeat("b", 64)}, BuiltFrom: BundleOrigin{Registry: "registry.example/team/real", Platform: "linux/amd64"}}
	if got, err := s.env.BundleLogin(t.Context(), copy, other); err != nil || got != nil {
		t.Fatalf("another repository's bundle got the copy's credential: %+v %v", got, err)
	}
}

// A project's release images are read with its registry's credential, or
// anonymously when it has no registry; a registry whose credential cannot
// be read is an error, never an anonymous read.
//
// Kills: the registry's read error taken as "no registry" (the Docker
// host's pull login, and the s3-bucket and App Platform release reads).
func TestAnUnreadableRegistryCredentialIsNotAnAnonymousRead(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	dir := t.TempDir()
	id := createTestProject(t, db, "shop", "Shop")
	projects := project.New(db, nil)
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects, Secrets: secret.NewService(db, secret.Config{DataDir: dir})})
	s := New(context.Background(), db, assembled(db, testRuntime{DataDir: dir, Projects: projects, Integrations: integrations}, Dependencies{}))
	p := &Providers{env: s.env}
	if got, err := s.env.ReleaseLogin(t.Context(), id); err != nil || got != nil {
		t.Fatalf("no registry attached: %+v %v", got, err)
	}
	if got, err := p.releaseCredential(t.Context(), id, "registry.example/team/shop:latest"); err != nil || got != (oci.Credential{}) {
		t.Fatalf("no registry attached: %+v %v", got, err)
	}
	if _, err := integrations.Bindings.Upsert(t.Context(), "registry", integration.CapContainerRegistry, "service", `{"url":"registry.example","username":"robot","tokenFrom":["REGISTRY_PASSWORD"]}`, false); err != nil {
		t.Fatal(err)
	}
	if err := integrations.Attachments.Attach(t.Context(), id, integration.CapContainerRegistry, "registry", `{"repo":"registry.example/team/shop"}`, integration.Entered, "test", nil); err != nil {
		t.Fatal(err)
	}
	// A folder where the secrets file belongs: the credential cannot be read.
	if err := os.Mkdir(filepath.Join(dir, "secrets.env"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := s.env.ReleaseLogin(t.Context(), id); err == nil || got != nil {
		t.Fatalf("an unreadable credential: %+v %v", got, err)
	}
	if got, err := p.releaseCredential(t.Context(), id, "registry.example/team/shop:latest"); err == nil || got != (oci.Credential{}) {
		t.Fatalf("an unreadable credential: %+v %v", got, err)
	}
}

func TestPublicationSourceCredentialUsesOneRepositoryAuthority(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		repo, source string
		want         bool
	}{
		{"docker.io/team/app", "index.docker.io/team/base:tag", true},
		{"index.docker.io/team/app", "docker.io/team/base:tag", true},
		{"registry.example/team/app", "REGISTRY.EXAMPLE/team/base:tag", true},
		{"registry.example:5000/team/app", "registry.example:5001/team/base:tag", false},
		{"registry.example/team/app", "other.example/team/base:tag", false},
		{"registry.example/team/app", "registry.example/team/base@sha256:abc", false},
	} {
		repo := oci.Repository{Name: c.repo, Credential: oci.Credential{Username: "robot", Password: "private-canary"}}
		got := sourceCredential(false, repo, c.source)
		if (got.Password == "private-canary") != c.want {
			t.Fatalf("repo=%s source=%s credential selected=%t", c.repo, c.source, got.Password != "")
		}
		if got := sourceCredential(true, repo, c.source); got != (oci.Credential{}) {
			t.Fatal("eval-selected image received a credential")
		}
	}
}

func TestRegistryDestinationRefusalReachesPublicationAndLogin(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	dir := t.TempDir()
	id := createTestProject(t, db, "shop", "Shop")
	projects := project.New(db, nil)
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects, Secrets: secret.NewService(db, secret.Config{DataDir: dir})})
	env := NewEnvironment(EnvironmentDependencies{Projects: projects, Integrations: integrations})
	if _, err := integrations.Bindings.Save(t.Context(), "registry", integration.CapContainerRegistry, "service", `{"url":"docker.io"}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := integrations.Attachments.Attach(t.Context(), id, integration.CapContainerRegistry, "registry", `{"repo":"docker.io/team/shop","username":"robot","token":"private-canary"}`, integration.Entered, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	repo, err := env.ReleaseRepository(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	login, err := env.ReleaseLogin(t.Context(), id)
	if err != nil || login == nil || login.Host != "index.docker.io" || sourceCredential(false, repo, "docker.io/team/base:tag") != repo.Credential {
		t.Fatalf("canonical resolver/selector: %+v %v", login, err)
	}
	if _, err := integrations.Bindings.Save(t.Context(), "registry", integration.CapContainerRegistry, "service", `{"url":"other.example"}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	if got, err := env.ReleaseRepository(t.Context(), id); err == nil || got != (oci.Repository{}) {
		t.Fatalf("unsafe publication repository: %+v %v", got, err)
	}
	if got, err := env.ReleaseLogin(t.Context(), id); err == nil || got != nil {
		t.Fatalf("unsafe Docker login: %+v %v", got, err)
	}
}

func TestHistoricalCredentialSelectionPreservesAuthorityAndEvalScope(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		repo, ref        string
		restricted, want bool
	}{
		{"docker.io/team/shop", "DOCKER.IO/team/shop:old", false, true},
		{"docker.io/team/shop", "index.docker.io/team/shop:old", true, true},
		{"registry.example/team/shop", "registry.example/other/shop:old", false, true},
		{"registry.example/team/shop", "registry.example/other/shop:old", true, false},
		{"registry.example/team/shop", "registry.example:443/team/shop:old", false, false},
		{"registry.example/team/shop", "other.example/team/shop:old", false, false},
		{"registry.example/team/shop", "https://registry.example/team/shop", false, false},
	} {
		if got := releaseCredentialAllows(oci.Repository{Name: c.repo}, c.ref, c.restricted); got != c.want {
			t.Errorf("repo=%s ref=%s restricted=%v selected=%v want=%v", c.repo, c.ref, c.restricted, got, c.want)
		}
	}
}
