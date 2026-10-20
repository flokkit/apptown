package deployment

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/oci"

	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/secret"
)

// A copy goes only on an eval target: this forge's own host when it has an
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
// publishes under the copy's own repository with the forge's credential.
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

// The forge's credential for a copy's own repository reaches that
// repository only: an image the copy's code names is read with no
// credential as it is published, however it is named, while an ordinary
// project's own-registry image is read with its credential.
func TestACopysCodeNamesNoImageTheForgesCredentialReads(t *testing.T) {
	t.Parallel()
	login := &docker.RegistryCredential{Host: "registry.example", Username: "robot", Token: "forge-held"}
	repo := oci.Repository{Name: "registry.example/evals/shop-copy", Credential: oci.Credential{Username: "robot", Password: "forge-held"}}
	for _, source := range []string{"registry.example/team/real-payroll:latest", "registry.example/evals/shop-copy:1"} {
		if got := sourceCredential(true, login, repo, source); got != (oci.Credential{}) {
			t.Fatalf("a copy reads %s with %+v", source, got)
		}
	}
	if got := sourceCredential(false, login, repo, "registry.example/team/real:1"); got.Password != "forge-held" {
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
	if got := s.env.BundleLogin(t.Context(), copy, own); got == nil || got.Token != "forge-held" || got.Host != "registry.example" {
		t.Fatalf("the copy's own bundle: %+v", got)
	}
	other := &ReleaseBundle{Parts: map[string]string{"web": "registry.example/team/real@sha256:" + strings.Repeat("b", 64)}, BuiltFrom: BundleOrigin{Registry: "registry.example/team/real", Platform: "linux/amd64"}}
	if got := s.env.BundleLogin(t.Context(), copy, other); got != nil {
		t.Fatalf("another repository's bundle got the copy's credential: %+v", got)
	}
}
