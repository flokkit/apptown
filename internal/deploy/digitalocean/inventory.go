package digitalocean

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/flokkit/apptown/internal/deploy"
)

// Inventory lists the account's apps, database clusters, registry and
// Spaces keys as the provider reports them. Spaces buckets are S3 objects
// the account's API does not list, so they are not here: a deployment's
// bucket is shown by its key, whose grant names it. A key is reported only
// to confirm a record, except a maker key an AppTown left (its name says so),
// which shows as unrecorded: a live full-access credential. An app or
// cluster without this installation's mark (InstanceEnv, InstallationTag)
// confirms a record only: another installation's, one made before the mark,
// or one AppTown never made (its own database) is never called a leftover.
func (i Inventory) Inventory(ctx context.Context) ([]deploy.Resource, error) {
	c, cfg, err := i.Client(ctx)
	if err != nil {
		return nil, err
	}
	var out []deploy.Resource
	var apps []doApp
	var dbs []doDatabase
	var reg struct {
		Registry struct {
			Name   string `json:"name"`
			Region string `json:"region"`
		} `json:"registry"`
	}
	// Three independent questions, asked at once: the page waits for the
	// slowest, not the sum.
	var wg sync.WaitGroup
	var appsErr, dbsErr, regErr, keysErr error
	var keys []spacesKey
	wg.Add(4)
	go func() { defer wg.Done(); keys, keysErr = spacesKeys(ctx, c) }()
	go func() { defer wg.Done(); apps, appsErr = listAll[doApp](ctx, c, "/v2/apps?per_page=200", "apps") }()
	go func() {
		defer wg.Done()
		dbs, dbsErr = listAll[doDatabase](ctx, c, "/v2/databases?per_page=200", "databases")
	}()
	go func() { defer wg.Done(); regErr = c.Call(ctx, "GET", "/v2/registry", nil, &reg) }()
	wg.Wait()
	if appsErr != nil {
		return nil, fmt.Errorf("listing apps: %w", appsErr)
	}
	if dbsErr != nil {
		return nil, fmt.Errorf("listing databases: %w", dbsErr)
	}
	if keysErr != nil {
		return nil, fmt.Errorf("listing Spaces keys: %w", keysErr)
	}
	for _, k := range keys {
		r := deploy.Resource{Kind: KindSpacesKey, Name: k.Name, ID: k.AccessKey, Status: "active", Detail: grantWords(k.Grants)}
		if !strings.HasPrefix(k.Name, "apptown-maker-") {
			r.Tags = []string{deploy.TagConfirmsOnly}
		}
		out = append(out, r)
	}
	for _, app := range apps {
		status := "no deployment"
		switch {
		case app.InProgressDeployment != nil:
			status = "deploying"
		case app.ActiveDeployment != nil:
			status = strings.ToLower(app.ActiveDeployment.Phase)
		}
		r := deploy.Resource{Kind: "app", Name: app.Spec.Name, ID: app.ID, Status: status, URL: app.LiveURL, Region: app.Region.Slug}
		marked := false
		for _, e := range app.Spec.Envs {
			marked = marked || i.Installation != "" && e.Key == InstanceEnv && e.Value == i.Installation
		}
		if !marked {
			r.Tags = []string{deploy.TagConfirmsOnly}
		}
		out = append(out, r)
	}
	for _, db := range dbs {
		r := deploy.Resource{Kind: "database", Name: db.Name, ID: db.ID, Status: db.Status, Region: db.Region, Size: db.Size,
			Version: db.Engine + " " + db.Version, Tags: db.Tags}
		if i.Installation == "" || !slices.Contains(db.Tags, InstallationTag(i.Installation)) {
			r.Tags = append(slices.Clone(db.Tags), deploy.TagConfirmsOnly)
		}
		out = append(out, r)
	}
	if regErr == nil && reg.Registry.Name != "" {
		r := deploy.Resource{Kind: "registry", Name: reg.Registry.Name, Region: reg.Registry.Region, Status: "ready", Record: deploy.RecordRecorded}
		type repository struct {
			Name     string `json:"name"`
			TagCount int    `json:"tag_count"`
		}
		if repos, err := listAll[repository](ctx, c, "/v2/registry/"+reg.Registry.Name+"/repositoriesV2?per_page=200", "repositories"); err == nil {
			names := make([]string, 0, len(repos))
			for _, rp := range repos {
				names = append(names, fmt.Sprintf("%s (%d tags)", rp.Name, rp.TagCount))
			}
			r.Detail = strings.Join(names, ", ")
		}
		if reg.Registry.Name == cfg.Registry {
			r.Hosts = []deploy.ResourceHost{{Project: "(this target's images)"}}
		}
		out = append(out, r)
	}
	return out, nil
}

// grantWords says what a key reaches, in words.
func grantWords(grants []spacesGrant) string {
	var out []string
	for _, g := range grants {
		if g.Bucket == "" {
			out = append(out, g.Permission+" on every bucket")
			continue
		}
		out = append(out, g.Permission+" on the bucket "+g.Bucket)
	}
	return strings.Join(out, ", ")
}
