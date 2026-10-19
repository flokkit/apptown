package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"flokkit.io/forge/internal/deploy"
)

// The project's DOCR repository (design/subsystems/deployment.md, "Removing
// a deployment") is the forge's on its account: the forge creates it, named after the
// project, with the first copy into it, and records that. It never adopts
// one: a repository of that name the forge did not record creating refuses
// the release. Every deployment of the project on an App Platform target
// with the same connector uses it; when the last of them is taken down,
// every image in it is deleted. The forge starts no garbage collection:
// while one runs DOCR takes no pushes, so one started at take-down would
// fail the next release. The space and the
// empty repository's name, which DOCR lists until a collection runs, are
// freed by a collection an operator runs or DigitalOcean schedules. An image someone else pushed into the
// forge's repository goes with the rest: don't push to the forge's
// repositories.

// RepositoryOwner records the project's repositories the forge created on
// this account (its connector).
type RepositoryOwner interface {
	Owns(ctx context.Context, registry, repository string) (bool, error)
	Created(ctx context.Context, registry, repository string) error
}

// ErrRepositoryNotOurs refuses a release whose project's repository exists
// in the account's registry but was not made by the forge.
func errRepositoryNotOurs(registry, repo string) error {
	return deploy.Terminal(fmt.Errorf("the container registry %s already has a repository named %s that AppTown did not create; nothing was created on DigitalOcean. Rename or remove that repository, or attach another account, then release again", registry, repo))
}

// RepositoryExists asks the registry whether it holds repo.
func RepositoryExists(ctx context.Context, c *Client, registry, repo string) (bool, error) {
	repos, err := listAll[struct {
		Name string `json:"name"`
	}](ctx, c, "/v2/registry/"+url.PathEscape(registry)+"/repositoriesV2?per_page=100", "repositories")
	if err != nil {
		return false, fmt.Errorf("listing the registry's repositories: %w", err)
	}
	for _, r := range repos {
		if r.Name == repo {
			return true, nil
		}
	}
	return false, nil
}

// claimRepository says whether the copies that follow make the project's
// repository (so the first that succeeds records it), or go into one the
// forge recorded creating. One the forge did not create is refused; one it
// cannot ask about is an error, never a guess.
func (d *Driver) claimRepository(ctx context.Context, c *Client, registry string) (creating bool, err error) {
	if d.Repository == nil {
		return false, nil
	}
	owned, err := d.Repository.Owns(ctx, registry, d.Slug)
	if err != nil {
		return false, fmt.Errorf("reading whether AppTown made the repository %s: %w", d.Slug, err)
	}
	if owned {
		return false, nil
	}
	exists, err := RepositoryExists(ctx, c, registry, d.Slug)
	if err != nil {
		return false, err
	}
	if exists {
		return false, errRepositoryNotOurs(registry, d.Slug)
	}
	return true, nil
}

// emptyRepository deletes every image in the project's repository, when this was the last deployment using it and
// the forge recorded creating it. It reports what it deleted, or what it
// left and why.
func (d *Driver) emptyRepository(ctx context.Context, td deploy.TakeDown) (deleted []string, left []deploy.Left) {
	if !td.LastUser || d.Repository == nil {
		return nil, nil
	}
	c, cfg, err := d.Client(ctx)
	if err != nil {
		return nil, []deploy.Left{repositoryLeft(cfg.Registry, d.Slug, err)}
	}
	registry, err := c.RegistryName(ctx)
	if err != nil || registry == "" {
		if err == nil {
			return nil, nil // no registry on the account: nothing was copied
		}
		return nil, []deploy.Left{repositoryLeft(cfg.Registry, d.Slug, err)}
	}
	owned, err := d.Repository.Owns(ctx, registry, d.Slug)
	if err != nil || !owned {
		if err != nil {
			return nil, []deploy.Left{repositoryLeft(registry, d.Slug, err)}
		}
		return nil, nil // not the forge's: never touched
	}
	base := "/v2/registry/" + url.PathEscape(registry) + "/repositories/" + url.PathEscape(d.Slug) + "/digests"
	manifests, err := listAll[struct {
		Digest string `json:"digest"`
	}](ctx, c, base+"?per_page=100", "manifests")
	if err != nil && !IsStatus(err, 404) {
		return nil, []deploy.Left{repositoryLeft(registry, d.Slug, fmt.Errorf("listing its images: %w", err))}
	}
	var failed []error
	n := 0
	for _, m := range manifests {
		if err := c.Call(ctx, "DELETE", base+"/"+url.PathEscape(m.Digest), nil, nil); err != nil && !IsStatus(err, 404) {
			failed = append(failed, fmt.Errorf("%s: %w", m.Digest, err))
			continue
		}
		n++
	}
	name := registry + "/" + d.Slug
	if len(failed) > 0 {
		left = append(left, repositoryLeft(registry, d.Slug, fmt.Errorf("deleted %d of %d images: %w", n, len(manifests), errors.Join(failed...))))
	}
	deleted = append(deleted, fmt.Sprintf("%d images in the image repository %s; the space, and the empty repository's name that DOCR keeps listed, are freed by the registry's next garbage collection", n, name))
	return deleted, left
}

func repositoryLeft(registry, repo string, err error) deploy.Left {
	return deploy.Left{What: "image repository", Handle: map[string]any{"kind": "repository", "name": registry + "/" + repo, "registry": registry, "repository": repo}, Why: err.Error()}
}
