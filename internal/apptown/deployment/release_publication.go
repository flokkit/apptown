package deployment

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/oci"
	"github.com/google/go-containerregistry/pkg/name"
)

// Releases built on the worker (design/operations/workers.md, "Releases
// built on the worker"). On an AppTown with a worker a landing has the worker build each
// distinct image of its release once, from the landed commit; AppTown
// publishes them to the project's container registry with its own
// credential, reads their digests back, and commits a bundle naming them
// (PublishRelease) before anything is deployed. Docker hosts pull those
// digests; nothing is built on a target.

// ReleaseImage is one distinct image of a release, under the build's name
// for it: part-<part> or init-<step>, after the part or init step that first
// declares it. The two prefixes keep a part's name and an init step's apart
// whatever they are called (a part named init-migrate and an init step
// migrate are different images).
type ReleaseImage struct {
	Name string
	// Build is the tree folder an image is built from (build:), and Static
	// the folder a static site's files are packed from, into the file
	// server's image, which serves them on Port (Dockerfile). Source is an
	// image-only declaration's reference (image:), copied into the registry
	// by digest. StaticBuild is a static site built first: its stage is
	// exported and its output folder packed as Static is. Exactly one of
	// Build, Static, StaticBuild and Source is set.
	Build, Static, Source string
	StaticBuild           appcontract.StaticBuild
	Port                  int
}

// IsStatic says the image is a static site's: the file server's image.
func (img ReleaseImage) IsStatic() bool {
	return img.Static != "" || img.StaticBuild != (appcontract.StaticBuild{})
}

// Dockerfile is a static site's image: its files in the file server's
// image, with the command that serves them (docker.StaticSiteDockerfile),
// as a Docker host's own build of it is (docker.PrepareImages).
func (img ReleaseImage) Dockerfile(server string) (string, error) {
	if server == "" {
		return "", fmt.Errorf("no static server image: the tool versions name it")
	}
	return docker.StaticSiteDockerfile(server, img.Port), nil
}

// ReleasePlan is what a release is made of: its distinct images, in the
// contract's order, and which of them each part and init step runs.
type ReleasePlan struct {
	Images      []ReleaseImage
	Parts, Init map[string]string // part or init step -> image name
}

// PlanRelease is the release c declares: one image for each distinct build,
// static site or image reference, shared by every part and init step that
// declares the same.
func PlanRelease(c *appcontract.Contract) ReleasePlan {
	plan := ReleasePlan{Parts: map[string]string{}, Init: map[string]string{}}
	named := map[ReleaseImage]string{}
	add := func(name string, img ReleaseImage) string {
		if n, ok := named[img]; ok {
			return n
		}
		named[img] = name
		img.Name = name
		plan.Images = append(plan.Images, img)
		return name
	}
	declared := func(build, static string, sb *appcontract.StaticBuild, image string, port int) ReleaseImage {
		switch {
		case sb != nil:
			return ReleaseImage{StaticBuild: *sb, Port: port}
		case static != "":
			return ReleaseImage{Static: static, Port: port}
		case build != "":
			return ReleaseImage{Build: build}
		}
		return ReleaseImage{Source: image}
	}
	for _, p := range c.Parts {
		plan.Parts[p.Name] = add("part-"+p.Name, declared(p.Build, p.Static, p.StaticBuild, p.Image, p.Port))
	}
	for _, it := range c.Init {
		plan.Init[it.Name] = add("init-"+it.Name, declared(it.Build, "", nil, it.Image, 0))
	}
	return plan
}

// ErrNoReleaseRegistry: a release built on the worker is published to the
// project's container-registry attachment, and it has none.
var ErrNoReleaseRegistry = fmt.Errorf("%w: this project has no container registry for its releases; attach one", ErrReleaseRefused)

// LandingRegistry is the repository a landing publishes its release to.
// A project with no container registry is first given the one
// container-registry connector AppTown offers, through the integrations
// owner (integration.AttachOfferedRegistry), as LandingTarget gives a
// landing the one offered Docker host; with none or several the refusal
// names the choice a person makes.
//
// An eval copy is attached nothing: its releases go to the repository the
// AppTown keeps for it (integration.Service.Registry), with AppTown's own
// credential.
func (s *Service) LandingRegistry(ctx context.Context, projectID ids.ID, who string, receipt map[string]any) (oci.Repository, error) {
	if s.env.integrations == nil {
		return oci.Repository{}, ErrNoReleaseRegistry
	}
	if purpose, err := s.env.Purpose(ctx, projectID); err != nil {
		return oci.Repository{}, err
	} else if !purpose.Integrations() {
		return s.env.ReleaseRepository(ctx, projectID)
	}
	if err := s.env.integrations.AttachOfferedRegistry(ctx, projectID, who, receipt); errors.Is(err, integration.ErrNoOfferedRegistry) {
		return oci.Repository{}, refused("this project has no container registry for its releases, and %s", strings.TrimPrefix(err.Error(), integration.ErrNoOfferedRegistry.Error()+": "))
	} else if err != nil {
		return oci.Repository{}, fmt.Errorf("attaching the offered container registry: %w", err)
	}
	return s.env.ReleaseRepository(ctx, projectID)
}

// TargetPlatform is the platform a Docker host runs images for, as its
// daemon answers, in canonical OCI form. A daemon that cannot be asked, or
// an answer that names no known platform, is an error, never a guess.
func (s *Service) TargetPlatform(ctx context.Context, t *Target) (string, error) {
	if t.Kind != KindDockerHost {
		return "", fmt.Errorf("%s (%s) is not a Docker host", t.Name, t.Kind)
	}
	dk, err := s.hosts.DockerScope(ctx, t)
	if err != nil {
		return "", err
	}
	out, err := dk.Command(ctx, docker.Command{Args: []string{"info", "--format", "{{.OSType}} {{.Architecture}}"}})
	if err != nil {
		return "", fmt.Errorf("asking %s's Docker for its platform: %w: %s", t.Name, err, strings.TrimSpace(string(out)))
	}
	osType, arch, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	platform, err := oci.Platform(osType, arch)
	if err != nil {
		return "", fmt.Errorf("%s's Docker names no known platform: %w", t.Name, err)
	}
	return platform, nil
}

// changeTag is the readable tag a landing's image is published under:
// change-<change>-<image>, or run-<run>-<image> for a run of no change.
func changeTag(changeID, runID ids.ID, image string) string {
	if changeID.IsZero() {
		return fmt.Sprintf("run-%d-%s", runID, image)
	}
	return fmt.Sprintf("change-%d-%s", changeID, image)
}

// tagRelease adds release-<n>-<image> to each image of a digest bundle in
// its registry, once release n of it is live: the same manifests under a
// readable name, for people and retention rules. A tag is not evidence (the
// bundle's digests are), so a tag that cannot be written is logged and
// never fails the release.
func (s *Service) tagRelease(ctx context.Context, number int64, b *ReleaseBundle, logf func(string, ...any)) {
	if b == nil || !b.ByDigest() {
		return
	}
	repo, err := s.env.ReleaseRepository(ctx, b.ProjectID)
	if err != nil {
		logf("not tagging release #%d's images: %v", number, err)
		return
	}
	purpose, err := s.env.Purpose(ctx, b.ProjectID)
	if err != nil {
		logf("not tagging release #%d's images: %v", number, err)
		return
	}
	if !releaseCredentialAllows(repo, b.BuiltFrom.Registry, !purpose.Integrations()) {
		logf("not tagging release #%d's images: their registry is outside the current credential's scope", number)
		return
	}
	repo.Name = b.BuiltFrom.Registry
	for _, images := range []struct {
		prefix string
		refs   map[string]string
	}{{"part-", b.Parts}, {"init-", b.Init}} {
		for name, ref := range images.refs {
			_, digest, _ := strings.Cut(ref, "@")
			tag := fmt.Sprintf("release-%d-%s%s", number, images.prefix, name)
			if err := oci.Tag(ctx, repo, digest, tag); err != nil {
				logf("could not tag %s as %s (a name for people; the release stands): %v", ref, tag, err)
			}
		}
	}
}

// ReleasePublication is a release the worker built, to publish and commit.
type ReleasePublication struct {
	// Log reports phase entry only; image names, never credentials or image contents.
	Log deploy.Logf
	// By is the deploy step attempt publishing it: it must own its step
	// before each image is written and when the bundle is committed.
	By       AttemptClaim
	ChangeID ids.ID
	Tree     string // the landed commit
	Contract []byte // its .apptown/app.yaml
	Plan     ReleasePlan
	// Built is each built image's export, by image name, as the worker
	// captured it. AppTown trusts none of it: what it records is what the
	// registry reads back.
	Built    map[string]oci.Layout
	Platform string
	Build    BundleBuild
}

// PublishRelease publishes every image of a release to the project's
// registry and commits the bundle naming them by the digests the registry
// read back, in one transaction with the check that the deploy step attempt
// still owns its step. Images are tagged change-<change>-<image> for people
// and retention rules (changeTag); a retry of the landing moves the tag to
// its own build, and the bundle records the digest.
// Before each image is written the attempt's ownership is checked, and ctx
// ends a transfer: an interrupted or superseded attempt commits no bundle,
// though an image already written stays in the registry, unused (the plan's
// accepted failure). An unreachable or refusing registry is an error,
// naming the registry's answer.
func (s *Service) PublishRelease(ctx context.Context, in ReleasePublication) (*ReleaseBundle, error) {
	if in.By == nil {
		return nil, errors.New("a release is published only by a claimed deploy step attempt")
	}
	projectID, runID, _, err := in.By.ClaimedStep("deploy")
	if err != nil {
		return nil, err
	}
	repo, err := s.env.ReleaseRepository(ctx, projectID)
	if err != nil {
		return nil, err
	}
	purpose, err := s.env.Purpose(ctx, projectID)
	if err != nil {
		return nil, err
	}
	withheld := !purpose.Integrations()
	published, registry := map[string]string{}, ""
	for _, img := range in.Plan.Images {
		if err := in.By.InAttempt("deploy", func(*database.Tx, int64) error { return nil }); err != nil {
			return nil, err
		}
		if in.Log != nil {
			in.Log("Publishing release image %s to the registry", img.Name)
		}
		tag := changeTag(in.ChangeID, runID, img.Name)
		var got oci.Image
		if img.Source != "" {
			// An image of the project's own registry is read with its
			// credential, as a Docker host pulls it (docker.LoginRegistry).
			from := oci.Source{Reference: img.Source, Credential: sourceCredential(withheld, repo, img.Source)}
			got, err = oci.Copy(ctx, from, repo, tag, in.Platform)
		} else if layout := in.Built[img.Name]; layout == nil {
			err = fmt.Errorf("the build left no image %s", img.Name)
		} else {
			got, err = oci.Publish(ctx, layout, repo, tag, in.Platform)
		}
		if err != nil {
			return nil, fmt.Errorf("publishing the release's image %s to %s: %w", img.Name, repo.Name, err)
		}
		published[img.Name], registry = got.Reference(), got.Repository
	}
	parts, init := map[string]string{}, map[string]string{}
	for part, img := range in.Plan.Parts {
		parts[part] = published[img]
	}
	for step, img := range in.Plan.Init {
		init[step] = published[img]
	}
	if in.Log != nil {
		in.Log("Recording the published release bundle")
	}
	id, err := s.Bundles.Cut(ctx, projectID, in.Tree, parts, init, in.Contract, BundleOrigin{ChangeID: in.ChangeID, RunID: runID,
		Registry: registry, Platform: in.Platform, Build: in.Build, By: in.By})
	if err != nil {
		return nil, fmt.Errorf("committing the release bundle: %w", err)
	}
	return s.Bundles.ByID(ctx, id)
}

// sourceCredential is the credential an image the app contract names is
// read with as it is published: the project's registry credential for an
// image of that registry, as a Docker host pulls it. An eval copy's code
// names its images, so for a project given no credentials (withheld)
// nothing is read with one: AppTown's credential reaches the copy's own
// repository and no other.
func sourceCredential(withheld bool, repo oci.Repository, source string) oci.Credential {
	registry, err := name.NewRepository(repo.Name)
	if withheld || err != nil || !oci.RegistryMatches(registry.RegistryStr(), source) {
		return oci.Credential{}
	}
	return repo.Credential
}

// PublishesLanding says whether a landing of projectID built on a Docker
// host is published (PublishLanded): when the project has a container
// registry. None is false; a registry that cannot be used (a credential
// named but not held) is the refusal, before anything is built.
func (s *Service) PublishesLanding(ctx context.Context, projectID ids.ID) (bool, error) {
	_, err := s.env.ReleaseRepository(ctx, projectID)
	if errors.Is(err, ErrNoReleaseRegistry) {
		return false, nil
	}
	return err == nil, err
}

// PublishLanded publishes what a landing built on its Docker host, as
// PublishRelease publishes what the worker built: a worker changes where a
// release is built, never what it produces. Each image of in.Plan that is
// built from the tree is the one the boot ran (LandingResult.Images),
// saved from the host's daemon (`docker save`, an OCI layout in a tar);
// image-only declarations are copied by PublishRelease as on the worker.
// The bundle names the host as where its images were built, for the
// platform its daemon runs. The saved files are removed before it returns.
func (s *Service) PublishLanded(ctx context.Context, landed LandingResult, in ReleasePublication) (*ReleaseBundle, error) {
	t, err := s.Targets.ByID(ctx, landed.TargetID)
	if err != nil {
		return nil, fmt.Errorf("reading the landing's target: %w", err)
	}
	if in.Platform, err = s.TargetPlatform(ctx, t); err != nil {
		return nil, err
	}
	in.Build = BundleBuild{Worker: t.Name, Role: "docker-host"}
	dk, err := s.hosts.DockerScope(ctx, t)
	if err != nil {
		return nil, err
	}
	ran := map[string]string{} // image name -> the image the boot ran
	parts, init := landed.Images(ctx)
	for part, img := range in.Plan.Parts {
		ran[img] = cmp.Or(ran[img], parts[part])
	}
	for step, img := range in.Plan.Init {
		ran[img] = cmp.Or(ran[img], init[step])
	}
	dir, err := os.MkdirTemp("", "apptown-release-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in.Built = map[string]oci.Layout{}
	for _, img := range in.Plan.Images {
		if img.Source != "" {
			continue
		}
		id := ran[img.Name]
		if id == "" {
			return nil, fmt.Errorf("the landing ran no image %s", img.Name)
		}
		if in.Log != nil {
			in.Log("Saving release image %s from %s", img.Name, t.Name)
		}
		file := filepath.Join(dir, img.Name+".tar")
		if err := dk.ToFile(ctx, file, "save", id); err != nil {
			return nil, fmt.Errorf("saving the release's image %s from %s: %w", img.Name, t.Name, err)
		}
		layout, err := oci.OpenTarLayout(file)
		if err != nil {
			return nil, fmt.Errorf("reading the release's image %s as saved: %w", img.Name, err)
		}
		defer layout.Close()
		in.Built[img.Name] = layout
	}
	return s.PublishRelease(ctx, in)
}

// releaseCredentialAllows keeps ordinary registry credentials on their authority
// and eval publication credentials on the copy's own repository.
func releaseCredentialAllows(repo oci.Repository, ref string, ownRepositoryOnly bool) bool {
	r, err := name.NewRepository(repo.Name)
	if err != nil || !oci.RegistryMatches(r.RegistryStr(), ref) {
		return false
	}
	if !ownRepositoryOnly {
		return true
	}
	source, err := name.ParseReference(ref)
	return err == nil && source.Context().RepositoryStr() == r.RepositoryStr()
}
