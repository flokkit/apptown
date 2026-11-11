package digitalocean

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
)

func (d *Driver) CollectionStore(ctx context.Context) (string, error) {
	c, _, err := d.Client(ctx)
	if err != nil {
		return "", err
	}
	registry, err := c.RegistryName(ctx)
	if err != nil {
		return "", err
	}
	if registry == "" {
		return "", fmt.Errorf("the account has no container registry")
	}
	return "docr/" + d.ConnectionID.String() + "/" + registry, nil
}
func (d *Driver) ImageStore(ctx context.Context, _ deploy.Placement) (string, error) {
	key, err := d.CollectionStore(ctx)
	if err != nil {
		return "", err
	}
	return key + "/" + d.Slug, nil
}
func (d *Driver) TrimImages(ctx context.Context, _ deploy.Placement, expired, keep []string) ([]string, []deploy.Left, error) {
	c, _, err := d.Client(ctx)
	if err != nil {
		return nil, nil, err
	}
	registry, err := c.RegistryName(ctx)
	if err != nil {
		return nil, nil, err
	}
	if registry == "" {
		return nil, nil, fmt.Errorf("the account has no container registry")
	}
	base := "/v2/registry/" + url.PathEscape(registry) + "/repositories/" + url.PathEscape(d.Slug) + "/digests"
	manifests, err := listAll[struct {
		Digest string `json:"digest"`
	}](ctx, c, base+"?per_page=100", "manifests")
	if isStatus(err, 404) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	digests := map[string]bool{}
	for _, ref := range keep {
		_, digest, _ := strings.Cut(ref, "@")
		digests[digest] = true
	}
	candidates := []string{}
	for _, m := range manifests {
		if !strings.HasPrefix(m.Digest, "sha256:") {
			return nil, nil, fmt.Errorf("DOCR answered an unreadable image digest")
		}
		if digests[m.Digest] {
			continue
		}
		if slices.ContainsFunc(expired, func(ref string) bool { _, digest, _ := strings.Cut(ref, "@"); return digest == m.Digest }) {
			candidates = append(candidates, m.Digest)
		}
	}
	var deleted []string
	var left []deploy.Left
	for _, digest := range candidates {
		ref := registry + "/" + d.Slug + "@" + digest
		if err := c.Call(ctx, "DELETE", base+"/"+url.PathEscape(digest), nil, nil); err != nil && !isStatus(err, 404) {
			why := err.Error()
			if isStatus(err, 405) {
				why = "this registry does not delete images; its space is kept"
			}
			left = append(left, deploy.Left{What: ref, Why: why})
		} else {
			deleted = append(deleted, ref)
		}
	}
	return deleted, left, nil
}
func (d *Driver) CollectImages(ctx context.Context) error {
	c, _, err := d.Client(ctx)
	if err != nil {
		return err
	}
	registry, err := c.RegistryName(ctx)
	if err != nil {
		return err
	}
	active, err := activeCollection(ctx, c, registry)
	if err != nil {
		return err
	}
	if active != nil {
		return nil
	}
	return c.Call(ctx, "POST", "/v2/registry/"+url.PathEscape(registry)+"/garbage-collection", map[string]any{"type": "unreferenced blobs only"}, nil)
}

func (d *Driver) HasImages(ctx context.Context, _ deploy.Placement, refs []string) (bool, error) {
	c, _, err := d.Client(ctx)
	if err != nil {
		return false, err
	}
	registry, err := c.RegistryName(ctx)
	if err != nil {
		return false, err
	}
	if registry == "" {
		return false, nil
	}
	rows, err := listAll[struct {
		Digest string `json:"digest"`
	}](ctx, c, "/v2/registry/"+url.PathEscape(registry)+"/repositories/"+url.PathEscape(d.Slug)+"/digests?per_page=100", "manifests")
	if isStatus(err, 404) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	held := map[string]bool{}
	for _, row := range rows {
		if row.Digest == "" {
			return false, fmt.Errorf("DOCR answered an unreadable image list")
		}
		held[row.Digest] = true
	}
	for _, ref := range refs {
		_, digest, _ := strings.Cut(ref, "@")
		if !held[digest] {
			return false, nil
		}
	}
	return true, nil
}
