package preview

import (
	"os"
	"strings"

	"context"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/worker"
	"maps"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// Release previews from a published bundle (design/operations/workers.md,
// "Previews on the worker"): a release whose bundle names its images by digest is previewed
// from those digests where changes' previews boot, here the worker; a
// release recorded on a daemon before then is refused in an AppTown with no
// local Docker, naming why.

// releasedFrom records a release of bundle b on the project's main, settled
// as released, and answers its number.
func releasedFrom(t *testing.T, s *Service, projectID, bundleID ids.ID, image string) int64 {
	t.Helper()
	target, err := s.targets.ByName(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.deployments.EnsureStanding(t.Context(), projectID, "main", target.ID, (deployment.Attributes{}).WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.bundles.ByID(context.Background(), bundleID)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := s.releases.Start(context.Background(), deployment.ReleaseDraft{Source: deployment.ReleaseFromBundle, ProjectID: projectID, DeploymentID: dep.ID, BundleID: bundleID, Deployment: "main", Revision: b.ContractRevision, Actor: "owner"}, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	outcomes := deployment.NewOutcomes(s.db, nil)
	at, _, err := outcomes.Admit(context.Background(), deployment.Start{DeploymentID: dep.ID, TargetID: target.ID, Key: fmt.Sprintf("fixture-%d", rel.ID), Inputs: deployment.Inputs{ReleaseID: rel.ID, Revision: b.ContractRevision, ImageDigest: image}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outcomes.Complete(context.Background(), deployment.Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", URL: "http://main"}}); err != nil {
		t.Fatal(err)
	}
	return rel.Number
}

// releasePreviewWorld is the worker preview world with release previews
// configured, the project's container registry attached (the preview
// credential), a commit of the fixture contract in the project's
// repository, and a digest bundle and a bundle of daemon image IDs cut from
// it, each released.
func releasePreviewWorld(t *testing.T) (s *Service, w *fakePreviewWorker, local func() []string, digest *deployment.ReleaseBundle, published, recorded int64) {
	t.Helper()
	return releasePreviewWorldWithSize(t, false)
}

func releasePreviewWorldWithSize(t *testing.T, size bool) (s *Service, w *fakePreviewWorker, local func() []string, digest *deployment.ReleaseBundle, published, recorded int64) {
	t.Helper()
	s, w, local, dir, projectID := workerPreviewWorld(t)
	s.authority = releasePreviewAuthorityFunc(func(context.Context, ids.ID) error { return nil })
	if err := s.integrations.Attachments.Attach(t.Context(), projectID, "container-registry", "missing",
		`{"repo":"registry.example.com/team/p","username":"robot","token":"preview-token"}`, integration.Entered, "test", nil); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "workspaces", "project-"+projectID.String())
	writePreviewBootFixture(t, repo)
	contract := resolvedStackFixture
	if size {
		contract = strings.Replace(contract, "  postgres: {}", "  mariadb: {}", 1) + "profiles:\n  preview:\n    mariadb: {size: small}\n"
		if err := os.WriteFile(filepath.Join(repo, ".apptown", "app.yaml"), []byte(contract), 0644); err != nil {
			t.Fatal(err)
		}
	}
	commit := gitCommitAll(t, repo, "release fixture")
	parts := map[string]string{"web": releaseRepo + digestA, "worker": releaseRepo + digestB}
	init := map[string]string{"migrate": releaseRepo + digestA}
	id, err := s.bundles.Cut(t.Context(), projectID, commit, parts, init, []byte(contract), digestOrigin(&claimedAttempt{db: s.db, project: projectID, run: idstest.N(7), step: idstest.N(70), at: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if digest, err = s.bundles.ByID(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	legacy, err := s.bundles.Cut(t.Context(), projectID, commit, map[string]string{"web": "sha256:web", "worker": "sha256:worker"}, map[string]string{"migrate": "sha256:web"}, []byte(contract), deployment.BundleOrigin{})
	if err != nil {
		t.Fatal(err)
	}
	published = releasedFrom(t, s, projectID, digest.ID, digest.Parts["web"])
	recorded = releasedFrom(t, s, projectID, legacy, "sha256:web")
	return s, w, local, digest, published, recorded
}

// A published release's preview boots on the worker from the bundle's
// images by digest, with the preview's prepared values and credential, and
// nothing runs on this machine's Docker. AppTown keeps its record: the
// worker's reconciliation leaves it, and visitors are forwarded there.
//
// Kills: booting a published release from its recorded image on this
// machine; a spec without the bundle's digests; a boot whose record is not
// kept (reconciled away, never forwarded).
func TestAPublishedReleasesPreviewBootsOnTheWorkerFromItsDigests(t *testing.T) {
	t.Parallel()
	s, w, local, b, published, _ := releasePreviewWorld(t)
	if err := s.db.InTx(t.Context(), func(tx *database.Tx) error {
		held, err := project.ByIDIn(t.Context(), tx, b.ProjectID)
		if err != nil {
			return err
		}
		next := "renamed"
		_, err = project.UpdateSettingsIn(t.Context(), tx, b.ProjectID, held.Slug, project.SettingsPatch{ExpectedRevision: &held.SettingsRevision, Slug: &next})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	out, err := s.RequestReleasePreview(context.Background(), ReleaseSelection{ProjectID: b.ProjectID, Number: published})
	if err != nil {
		t.Fatal(err)
	}
	label := docker.ReleasePreviewLabel("p", published)
	name := docker.PreviewLabelName(label)
	if out.URL != "https://"+label+".preview.example" || out.Image != fmt.Sprintf("bundle %d", b.ID) {
		t.Fatalf("the answer: %+v", out)
	}
	if len(w.boots) != 1 {
		t.Fatalf("the worker was asked %d boots", len(w.boots))
	}
	spec, prepared := w.boots[0], w.prepared[0]
	if prepared.Origins != "https://"+label+".preview.example" {
		t.Fatalf("renamed historical preview origins: %q", prepared.Origins)
	}
	if spec.Name != name || spec.StackID != docker.ReleaseStackID("p", published) || spec.Images == nil ||
		!maps.Equal(spec.Images.Parts, b.Parts) || !maps.Equal(spec.Images.Init, b.Init) {
		t.Fatalf("the worker was asked %+v", spec)
	}
	if prepared.Contract == nil || prepared.Registry == nil || prepared.Registry.Token != "preview-token" {
		t.Fatalf("the boot's prepared values: %+v", prepared)
	}
	if runs := local(); len(runs) != 0 {
		t.Fatalf("this machine's Docker ran %q", runs)
	}
	if err := s.ReconcileWorkerPreviews(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !w.holds(name) || !s.ServedOnWorker(name) {
		t.Fatalf("the release's preview is not kept as one AppTown booted: held %v, recorded %v", w.holds(name), s.ServedOnWorker(name))
	}
}

// In an AppTown with no local Docker, a published release is still previewed
// from its digests, and a release recorded on a daemon before releases were
// published is refused with the reason, before anything boots.
//
// Kills: refusing every release preview in the mode (8a's refusal); a
// legacy release's preview attempted with no local Docker.
func TestInTheNoLocalDockerModeOnlyPublishedReleasesArePreviewed(t *testing.T) {
	t.Parallel()
	s, w, local, b, published, recorded := releasePreviewWorld(t)
	// As an AppTown started with -no-local-docker.
	s.hosts = deployment.NewHosts(s.targets, nil, deployment.HostSettings{Scope: s.hosts.Local(), DataDir: s.dataDir, BuiltInTarget: s.hosts.BuiltInTarget(), NoLocalDocker: true})
	if _, err := s.RequestReleasePreview(context.Background(), ReleaseSelection{ProjectID: b.ProjectID, Number: recorded}); !errors.Is(err, ErrReleaseNoLocalDocker) {
		t.Fatalf("a release recorded on a daemon: %v", err)
	}
	if len(w.boots) != 0 || len(local()) != 0 {
		t.Fatalf("a refused preview booted: worker %+v, local %v", w.boots, local())
	}
	if _, err := s.RequestReleasePreview(context.Background(), ReleaseSelection{ProjectID: b.ProjectID, Number: published}); err != nil {
		t.Fatalf("a published release: %v", err)
	}
	if len(w.boots) != 1 || w.boots[0].Images == nil || !maps.Equal(w.boots[0].Images.Parts, b.Parts) {
		t.Fatalf("the worker was asked %+v", w.boots)
	}
}

// A release recorded before digest bundles boots on this AppTown's own
// Docker even with a worker configured, and is reached here, not forwarded
// to the worker.
func TestALocalReleasePreviewIsNotRoutedToTheWorker(t *testing.T) {
	t.Parallel()
	s, w, local, b, _, recorded := releasePreviewWorld(t)
	if _, err := s.RequestReleasePreview(context.Background(), ReleaseSelection{ProjectID: b.ProjectID, Number: recorded}); err != nil {
		t.Fatal(err)
	}
	if len(local()) == 0 || len(w.boots) != 0 {
		t.Fatal("fixture: the preview did not boot here")
	}
	wantOrigin := "APPTOWN_ORIGINS=https://" + docker.ReleasePreviewLabel("p", recorded) + ".preview.example"
	if !strings.Contains(strings.Join(local(), "\n"), wantOrigin) || strings.Contains(strings.Join(local(), "\n"), "https://*.preview.example") {
		t.Fatalf("local historical preview origins: %v", local())
	}
	if name := docker.PreviewLabelName(docker.ReleasePreviewLabel("p", recorded)); s.ServedOnWorker(name) {
		t.Fatalf("%s is routed to the worker", name)
	}
}

const (
	releaseRepo = "registry.example.com/team/game"
	digestA     = "@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB     = "@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func digestOrigin(by deployment.AttemptClaim) deployment.BundleOrigin {
	return deployment.BundleOrigin{Registry: releaseRepo, Platform: "linux/amd64", Build: deployment.BundleBuild{Worker: "http://worker:7070", Role: "agent", Qualification: "design/qualification/worker-vm.md"}, By: by}
}

// claimedAttempt is Work's claim of a deploy step attempt, which a digest
// bundle is committed under: its fence's transaction, always held.
type claimedAttempt struct {
	db                 *database.DB
	project, run, step ids.ID
	at                 int64
}

func (c *claimedAttempt) ClaimedStep(kind string) (ids.ID, ids.ID, ids.ID, error) {
	if kind != "deploy" {
		return ids.Nil, ids.Nil, ids.Nil, errors.New("not a deploy step")
	}
	return c.project, c.run, c.step, nil
}

func (c *claimedAttempt) InAttempt(kind string, write func(*database.Tx, int64) error) error {
	if kind != "deploy" {
		return errors.New("not a deploy step")
	}
	return c.db.InTx(context.Background(), func(tx *database.Tx) error { return write(tx, c.at) })
}

// sizedLocalRelease is a release recorded before digest bundles, its web
// part declaring 4G, in a world whose previews otherwise run on a worker:
// its preview boots on this machine's Docker.
func sizedLocalRelease(t *testing.T) (*Service, *fakePreviewWorker, *world, func() []string, ids.ID, int64) {
	t.Helper()
	wd := newWorld(t, "", "")
	s, w, local, dir, projectID := onWorker(t, wd)
	s.authority = releasePreviewAuthorityFunc(func(context.Context, ids.ID) error { return nil })
	repo := filepath.Join(dir, "workspaces", "project-"+projectID.String())
	writePreviewBootFixture(t, repo)
	sized := strings.Replace(resolvedStackFixture, "    public: true\n", "    public: true\n    resources: { memory: 4G }\n", 1)
	if err := os.WriteFile(filepath.Join(repo, ".apptown", "app.yaml"), []byte(sized), 0o644); err != nil {
		t.Fatal(err)
	}
	commit := gitCommitAll(t, repo, "release fixture")
	legacy, err := s.bundles.Cut(t.Context(), projectID, commit, map[string]string{"web": "sha256:web", "worker": "sha256:worker"}, map[string]string{"migrate": "sha256:web"}, []byte(sized), deployment.BundleOrigin{})
	if err != nil {
		t.Fatal(err)
	}
	return s, w, wd, local, projectID, releasedFrom(t, s, projectID, legacy, "sha256:web")
}

// A release preview that boots on this machine is admitted by this
// machine's daemon, whatever the worker could hold: it boots when this
// machine can hold it though the worker cannot, and is refused with
// nothing removed when this machine cannot though the worker could.
func TestALocalReleasePreviewIsAdmittedByTheDaemonItRunsOn(t *testing.T) {
	t.Parallel()
	s, w, wd, local, projectID, recorded := sizedLocalRelease(t)
	wd.client.SetHostInfo(8<<30, 8, "/var/lib/docker")
	w.refuse = &worker.AdmissionRefused{Reason: "part web declares 4G of memory; this Docker host has 2G"}
	if _, err := s.RequestReleasePreview(context.Background(), ReleaseSelection{ProjectID: projectID, Number: recorded}); err != nil {
		t.Fatalf("a release this machine can hold: %v", err)
	}
	if len(local()) == 0 || w.admitted != 0 {
		t.Fatalf("booted here %d times; the worker was asked %d times", len(local()), w.admitted)
	}

	s, w, wd, _, projectID, recorded = sizedLocalRelease(t)
	wd.client.SetHostInfo(2<<30, 8, "/var/lib/docker")
	if _, err := s.RequestReleasePreview(context.Background(), ReleaseSelection{ProjectID: projectID, Number: recorded}); err == nil || !strings.Contains(err.Error(), "this Docker host has 2G") {
		t.Fatalf("a release this machine cannot hold: %v", err)
	}
	if got := append(wd.client.Matching("rm "), wd.client.Matching("run ")...); len(got) != 0 || w.admitted != 0 {
		t.Fatalf("before the refusal: %v; the worker was asked %d times", got, w.admitted)
	}
}

func TestAPublishedReleasePreviewSelectsItsNeedSizeOnce(t *testing.T) {
	t.Parallel()
	s, w, local, b, published, _ := releasePreviewWorldWithSize(t, true)
	if _, err := s.RequestReleasePreview(t.Context(), ReleaseSelection{ProjectID: b.ProjectID, Number: published}); err != nil {
		t.Fatal(err)
	}
	if len(w.prepared) != 1 || len(local()) != 0 {
		t.Fatalf("unexpected worker/local boots: %d %v", len(w.prepared), local())
	}
	prepared := w.prepared[0]
	if len(prepared.Needs) != 1 || prepared.Needs[0].Resources.MemoryBytes != 128<<20 || !strings.Contains(strings.Join(prepared.Needs[0].Command, " "), "--innodb-buffer-pool-size=32M") {
		t.Fatalf("release preview lost its selected size: %+v", prepared.Needs)
	}
}

// A release preview on this machine is kept only as the identity it was
// booted with: what it boots (its bundle, or its recorded image) and the
// prepared values. The same values hand back the running look; a secret
// set to another value since replaces it with a look stamped with the new
// identity. Before, any running look was handed back.
func TestALocalReleasePreviewIsReplacedWhenItsSecretChanges(t *testing.T) {
	t.Parallel()
	wd := newWorld(t, "", "")
	s := wd.service(Dependencies{})
	repo := filepath.Join(wd.dir, "workspaces", "project-"+wd.projectID.String())
	writePreviewBootFixture(t, repo)
	if err := os.WriteFile(filepath.Join(repo, secret.SpecFile), []byte("[project]\nname = \"p\"\nrevision = \"1.0\"\n\n[profiles.default]\nGREETING = { description = \"what the app says\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit := gitCommitAll(t, repo, "release fixture")
	bundleID, err := s.bundles.Cut(t.Context(), wd.projectID, commit, map[string]string{"web": "sha256:built", "worker": "sha256:built"}, map[string]string{"migrate": "sha256:built"}, []byte(resolvedStackFixture), deployment.BundleOrigin{})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := s.bundles.ByID(t.Context(), bundleID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.InTx(t.Context(), func(tx *database.Tx) error {
		held, err := project.ByIDIn(t.Context(), tx, wd.projectID)
		if err != nil {
			return err
		}
		next := "renamed"
		_, err = project.UpdateSettingsIn(t.Context(), tx, wd.projectID, held.Slug, project.SettingsPatch{ExpectedRevision: &held.SettingsRevision, Slug: &next})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := secret.Scope("p", "preview", "")
	set := func(value string) {
		t.Helper()
		if err := wd.secrets.Vault.Delete(t.Context(), scope, "GREETING"); err != nil {
			t.Fatal(err)
		}
		if _, err := wd.secrets.Vault.Put(t.Context(), scope, "GREETING", value, "test"); err != nil {
			t.Fatal(err)
		}
		if err := wd.secrets.Bindings.Put(t.Context(), wd.projectID, secret.Binding{Deployment: "preview", Name: "GREETING", Store: secret.StoreInternal, Ref: scope, Version: "1", UpdatedBy: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, release := range []Release{
		{ProjectID: wd.projectID, Number: 1, SourceRevision: commit, Bundle: bundle},
		{ProjectID: wd.projectID, Number: 2, SourceRevision: commit, Image: "reg.example/app:1"},
	} {
		name := docker.PreviewLabelName(docker.ReleasePreviewLabel("p", release.Number))
		boot := func() ReleaseResult {
			t.Helper()
			wd.client.ResetCommands()
			out, err := releaseBoots{service: s}.BootReleasePreview(t.Context(), release)
			if err != nil {
				t.Fatalf("release %d: %v", release.Number, err)
			}
			return out
		}
		set("hello")
		if out := boot(); out.Reused || len(wd.client.Matching("run -d --name "+name)) != 1 {
			t.Fatalf("release %d: the first look did not boot: %+v", release.Number, out)
		}
		if out := boot(); !out.Reused || len(wd.client.Matching("run ")) != 0 {
			t.Fatalf("release %d: the same values did not hand back the running look: %+v %v", release.Number, out, wd.client.Matching("run "))
		}
		set("goodbye")
		out := boot()
		runs := wd.client.Matching("run -d --name " + name)
		if out.Reused || len(runs) != 1 || len(wd.client.Matching("rm -f -v "+name)) == 0 || !strings.Contains(strings.Join(runs[0], " "), "GREETING=goodbye") {
			t.Fatalf("release %d: a changed secret kept the running look: %+v %v", release.Number, out, wd.client.Commands())
		}
	}
}
