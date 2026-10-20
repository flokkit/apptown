package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/oci"
	"flokkit.io/forge/internal/strs"
)

// --- the registry ----------------------------------------------------------------------------

// ensureRegistry makes the provider's DOCR registry exist and returns its
// name. One registry per account; the starter tier holds one repository.
func (d *Driver) ensureRegistry(ctx context.Context, c *Client, cfg Config, logf func(string, ...any)) (string, error) {
	var got struct {
		Registry struct {
			Name   string `json:"name"`
			Region string `json:"region"`
		} `json:"registry"`
	}
	err := c.Call(ctx, "GET", "/v2/registry", nil, &got)
	if err == nil && got.Registry.Name != "" {
		if cfg.Registry != "" && cfg.Registry != got.Registry.Name {
			return "", deploy.Terminal(fmt.Errorf("the account's registry is %q, the connector names %q — one registry per account; fix the connector", got.Registry.Name, cfg.Registry))
		}
		return got.Registry.Name, nil
	}
	if err != nil && !IsStatus(err, 404) {
		return "", err
	}
	name := cfg.Registry
	if name == "" {
		return "", deploy.Terminal(errors.New("no registry on the account and none named on the connector — set `registry` to the name to create"))
	}
	logf("creating the container registry %s (starter tier)", name)
	body := map[string]any{"name": name, "subscription_tier_slug": "starter", "region": doDBRegion(cfg.Region)}
	if err := c.Call(ctx, "POST", "/v2/registry", body, &got); err != nil {
		// The region is a preference; the default region is fine.
		delete(body, "region")
		if err2 := c.Call(ctx, "POST", "/v2/registry", body, &got); err2 != nil {
			return "", fmt.Errorf("creating the registry: %w", err)
		}
	}
	return got.Registry.Name, nil
}

// pushImage tags a local image for the registry and pushes it; the
// digest the registry reports is what the app spec names. The token is
// handed to docker login on stdin, never on a command line.
func (d *Driver) pushImage(ctx context.Context, c *Client, registry, repo, localImage, tag string, logf func(string, ...any)) (string, error) {
	host := d.options.RegistryHost
	if out, err := d.Docker.Command(ctx, []string{"login", host, "-u", "apptown", "--password-stdin"}, strings.NewReader(c.Token)); err != nil {
		return "", fmt.Errorf("logging in to %s: %s", host, strs.Truncate(redactToken(string(out), c.Token), 200))
	}
	ref := fmt.Sprintf("%s/%s/%s:%s", host, registry, repo, tag)
	if out, err := d.Docker.Command(ctx, []string{"tag", localImage, ref}, nil); err != nil {
		return "", fmt.Errorf("tagging %s: %s", strs.ShortSHA(localImage), strs.Truncate(string(out), 200))
	}
	logf("pushing %s (%s)", ref, strs.ShortSHA(localImage))
	if out, err := d.Docker.Command(ctx, []string{"push", ref}, nil); err != nil {
		return "", fmt.Errorf("pushing %s: %s", ref, strs.Truncate(redactToken(string(out), c.Token), 300))
	}
	digest := d.Docker.ImageDigest(ctx, ref)
	if digest == "" {
		return "", fmt.Errorf("pushed %s but docker reports no digest for it", ref)
	}
	// imageDigest returns repo@sha256:...; the spec wants the bare digest.
	if i := strings.Index(digest, "@"); i >= 0 {
		digest = digest[i+1:]
	}
	return digest, nil
}

// copyImage copies a release's image by digest (<repository>@sha256:<hex>)
// from its registry into DOCR, tagged tag, and answers the digest DOCR
// confirms: the image's own, so the app spec names exactly what the
// release recorded. AppTown's Docker is not used. The copy is written
// before the app changes; a failed one fails the release, and releasing the
// same bundle again copies what is missing.
func (d *Driver) copyImage(ctx context.Context, c *Client, registry, repo, ref, tag string, logf func(string, ...any)) (string, error) {
	from := oci.Source{Reference: ref}
	if d.ReleaseLogin != nil {
		from.Credential = d.ReleaseLogin()
	}
	return d.options.CopyImage(d, ctx, c, registry, repo, from, tag, logf)
}

// GateRepository is the DOCR repository AppTown's gate image is copied
// into: one per registry, shared by every project's gated deployments. App
// Platform takes one version of a DOCR repository per app (a second digest
// of the same repository is refused at create, "not configured for this
// app"), so the gate cannot share the project's repository. The name has
// an underscore, which no project slug has, so it is never a project's
// repository. It takes one of the registry's included repositories once.
// AppTown never empties it: it holds only the gate images, pinned by
// digest, and a take-down leaves them for the next gated deployment; an
// image a new gate release leaves unused stays until an operator deletes
// it. A repository of this name AppTown did not make is used as it is:
// the copy is by digest and DOCR must answer that digest.
const GateRepository = "apptown_gate"

// copyGateImage copies AppTown's gate image into GateRepository by its
// digest, read with GateLogin's credential for it (none: anonymously).
func (d *Driver) copyGateImage(ctx context.Context, c *Client, registry, ref string, logf func(string, ...any)) (doImage, error) {
	from := oci.Source{Reference: ref}
	if d.GateLogin != nil {
		from.Credential = d.GateLogin(ref)
	}
	_, digest, _ := strings.Cut(ref, "@sha256:")
	got, err := d.options.CopyImage(d, ctx, c, registry, GateRepository, from, "gate-"+strs.ShortSHA(digest), logf)
	if err != nil {
		return doImage{}, err
	}
	return doImage{RegistryType: "DOCR", Repository: GateRepository, Digest: got}, nil
}

// oneVersionPerRepository refuses a spec naming more than one version
// (digest or tag) of one DOCR repository: App Platform accepts such a spec
// at propose and refuses it at create, after AppTown has made the app's
// databases. The release is refused for this at admission
// (MultiImageRefusal); this is the backstop before the app is asked for.
func oneVersionPerRepository(spec AppSpec) error {
	versions := map[string]map[string]bool{}
	for _, cs := range [][]Component{spec.Services, spec.Workers, spec.Jobs} {
		for _, c := range cs {
			if c.Image.RegistryType != "DOCR" {
				continue
			}
			version := c.Image.Digest
			if version == "" {
				version = c.Image.Tag
			}
			if versions[c.Image.Repository] == nil {
				versions[c.Image.Repository] = map[string]bool{}
			}
			versions[c.Image.Repository][version] = true
		}
	}
	for _, v := range versions {
		if len(v) > 1 {
			return deploy.Terminal(errors.New(multiImageWords(len(v))))
		}
	}
	return nil
}

func (d *Driver) copyFrom(ctx context.Context, c *Client, registry, repo string, from oci.Source, tag string, logf func(string, ...any)) (string, error) {
	ref := from.Reference
	// DOCR takes the API token as the password, as `docker login` gives it.
	to := oci.Repository{Name: d.options.RegistryHost + "/" + registry + "/" + repo, Credential: oci.Credential{Username: "apptown", Password: c.Token}}
	logf("copying %s into %s:%s", ref, to.Name, tag)
	got, err := oci.Copy(ctx, from, to, tag, Platform)
	if err != nil {
		msg := fmt.Sprintf("copying %s into %s: %s", ref, to.Name, redactToken(err.Error(), c.Token))
		// DOCR refuses every push while it collects garbage, with the same
		// 401 a wrong credential gets. A refusal DOCR answered (the registry
		// or its token service) while a collection runs says both facts;
		// which caused it is not known. One the source answered is the
		// source's, whenever in the copy it came.
		if host, ok := oci.RefusedBy(err); ok && (host == d.options.RegistryHost || host == docrTokenHost) {
			if gc, gerr := activeCollection(ctx, c, registry); gerr == nil && gc != nil {
				msg += fmt.Sprintf(". The container registry %s was collecting garbage (%s) when it refused; DOCR refuses pushes while a collection runs, so release again once it has ended. If the refusal persists after it ends, the credential is at fault", registry, gc)
			}
		}
		return "", errors.New(msg)
	}
	if _, want, _ := strings.Cut(ref, "@"); got.Digest != want {
		return "", fmt.Errorf("copying %s into %s: DOCR answered %s", ref, to.Name, got.Digest)
	}
	return got.Digest, nil
}

// MultiImageRefusal says why a release whose images (parts and init steps
// by name) are not all one image cannot go to App Platform, "" when they
// are. AppTown copies every part and init image into the project's one
// DOCR repository, and App Platform runs one image per repository, so the
// remedies are one image for every part and init step, or another target.
// A release is refused on it at admission, before any provider effect.
func MultiImageRefusal(images ...map[string]string) string {
	distinct := map[string]bool{}
	for _, m := range images {
		for _, ref := range m {
			distinct[ref] = true
		}
	}
	if len(distinct) < 2 {
		return ""
	}
	return multiImageWords(len(distinct))
}

func multiImageWords(n int) string {
	return fmt.Sprintf("App Platform runs one image per repository, and this release's parts and init steps use %d different images in the project's one repository. Deploy it to a Docker host target instead.", n)
}

// docrTokenHost answers DOCR's push token requests; a refused credential
// for DOCR can come from it as well as from the registry.
const docrTokenHost = "api.digitalocean.com"

// registryCollection is a garbage collection DOCR is running on the
// account's registry (an operator's, or one DigitalOcean schedules). While
// one runs the registry is read-only: it answers a push token request 401,
// as if the credential were wrong.
type registryCollection struct {
	UUID      string    `json:"uuid"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func (g registryCollection) String() string {
	return fmt.Sprintf("started %s, %s", g.CreatedAt.UTC().Format(time.RFC3339), g.Status)
}

// activeCollection is the garbage collection running on registry, nil when
// none is.
func activeCollection(ctx context.Context, c *Client, registry string) (*registryCollection, error) {
	var got struct {
		GC registryCollection `json:"garbage_collection"`
	}
	err := c.Call(ctx, "GET", "/v2/registry/"+url.PathEscape(registry)+"/garbage-collection", nil, &got)
	if IsStatus(err, 404) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	switch got.GC.Status {
	case "", "succeeded", "failed", "cancelled":
		return nil, nil
	}
	return &got.GC, nil
}

func redactToken(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "<token>")
}

// RegistryRoom is how much of the account's registry is used: its
// subscription tier, the repositories the tier includes (0: no limit) and
// the repositories it holds. The starter tier holds one repository and the
// basic tier five; each project released to App Platform takes one, named
// after its slug, emptied when its last deployment there is taken down,
// and the first gated deployment (a restored copy) takes GateRepository.
type RegistryRoom struct {
	Tier         string
	Included     int
	Repositories []string
}

// Full: every included repository is taken.
func (r RegistryRoom) Full() bool { return r.Included > 0 && len(r.Repositories) >= r.Included }

// Holds says whether the registry already has repo.
func (r RegistryRoom) Holds(repo string) bool {
	for _, name := range r.Repositories {
		if name == repo {
			return true
		}
	}
	return false
}

func (r RegistryRoom) String() string {
	if r.Included <= 0 {
		return fmt.Sprintf("%d repositories, no limit, %s tier", len(r.Repositories), r.Tier)
	}
	return fmt.Sprintf("%d of %d repositories used, %s tier", len(r.Repositories), r.Included, r.Tier)
}

// RegistryRoom reads the registry's subscription and its repositories.
func (c *Client) RegistryRoom(ctx context.Context, registry string) (RegistryRoom, error) {
	var sub struct {
		Subscription struct {
			Tier struct {
				Slug                 string `json:"slug"`
				IncludedRepositories int    `json:"included_repositories"`
			} `json:"tier"`
		} `json:"subscription"`
	}
	if err := c.Call(ctx, "GET", "/v2/registry/subscription", nil, &sub); err != nil {
		return RegistryRoom{}, fmt.Errorf("reading the registry's subscription: %w", err)
	}
	repos, err := listAll[struct {
		Name string `json:"name"`
	}](ctx, c, "/v2/registry/"+url.PathEscape(registry)+"/repositoriesV2?per_page=100", "repositories")
	if err != nil {
		return RegistryRoom{}, fmt.Errorf("listing the registry's repositories: %w", err)
	}
	room := RegistryRoom{Tier: sub.Subscription.Tier.Slug, Included: sub.Subscription.Tier.IncludedRepositories, Repositories: []string{}}
	for _, r := range repos {
		room.Repositories = append(room.Repositories, r.Name)
	}
	return room, nil
}

// Preflight refuses, before anything is created or copied, a release that
// would send a value App Platform rewrites (unrewritable), and one whose
// images would need a new repository in a registry whose plan is full: the
// copy into DOCR would fail only after the database was made. It is the one
// refusal for a full registry. A registry the
// account does not have yet is created empty on this release. A registry
// whose room cannot be read is logged as unknown and the release goes on.
func (d *Driver) Preflight(ctx context.Context, p deploy.Placement, src deploy.Source, secrets deploy.ConsumerSecrets, logf deploy.Logf) error {
	if err := unrewritable(src.Contract, secrets); err != nil {
		return err
	}
	client, _, err := d.Client(ctx)
	if err != nil {
		return err
	}
	name, err := client.RegistryName(ctx)
	if err == nil && name == "" {
		return nil
	}
	var room RegistryRoom
	if err == nil {
		room, err = client.RegistryRoom(ctx, name)
	}
	if err != nil {
		logf("the container registry's room is unknown (%s); going on — copying the images fails if it has no room", strs.Truncate(redactToken(err.Error(), client.Token), 200))
		return nil
	}
	logf("container registry %s: %s", name, room)
	if room.Holds(d.Slug) && d.Repository != nil {
		if owned, err := d.Repository.Owns(ctx, name, d.Slug); err == nil && !owned {
			return errRepositoryNotOurs(name, d.Slug)
		}
	}
	// A gated deployment's gate image has its own repository
	// (GateRepository), made by the first gated deployment on the account.
	var missing []string
	if !room.Holds(d.Slug) {
		missing = append(missing, d.Slug)
	}
	if p.Gate != nil && !room.Holds(GateRepository) {
		missing = append(missing, GateRepository)
	}
	if room.Included <= 0 || len(room.Repositories)+len(missing) <= room.Included {
		return nil
	}
	return deploy.Terminal(fmt.Errorf("registry plan full (%d of %d repositories, %s tier): the container registry %s has no repository for %s yet and room for %d more, and nothing was created on DigitalOcean. Upgrade the registry plan, or take a deployment down (a project's repository is freed when its last deployment on this account goes and the registry's garbage collection has run), then release again", len(room.Repositories), room.Included, room.Tier, name, strings.Join(missing, " or "), room.Included-len(room.Repositories)))
}
