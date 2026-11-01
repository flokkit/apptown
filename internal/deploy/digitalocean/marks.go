package digitalocean

import (
	"context"
	"fmt"
	"net/url"
	"slices"
)

// MarkClusters gives every listed database cluster that lacks it the
// installation's tag (InstallationTag), in place: clusters made before
// AppTown tagged them stay accounted for after their records go. It answers
// how many clusters it tagged; a cluster no longer on the account is not
// counted.
func MarkClusters(ctx context.Context, c *Client, installation string, clusters []string) (int, error) {
	if installation == "" || len(clusters) == 0 {
		return 0, nil
	}
	tag := InstallationTag(installation)
	dbs, err := listAll[doDatabase](ctx, c, "/v2/databases?per_page=200", "databases")
	if err != nil {
		return 0, fmt.Errorf("listing database clusters: %w", err)
	}
	var untagged []map[string]string
	for _, db := range dbs {
		if slices.Contains(clusters, db.ID) && !slices.Contains(db.Tags, tag) {
			untagged = append(untagged, map[string]string{"resource_id": db.ID, "resource_type": "database"})
		}
	}
	if len(untagged) == 0 {
		return 0, nil
	}
	// A tag is made before it is assigned; making one that exists is
	// refused as unprocessable, and that tag is there to assign.
	if err := c.Call(ctx, "POST", "/v2/tags", map[string]string{"name": tag}, nil); err != nil && !isStatus(err, 422) {
		return 0, fmt.Errorf("making the tag %s: %w", tag, err)
	}
	if err := c.Call(ctx, "POST", "/v2/tags/"+url.PathEscape(tag)+"/resources", map[string]any{"resources": untagged}, nil); err != nil {
		return 0, fmt.Errorf("tagging %d database clusters %s: %w", len(untagged), tag, err)
	}
	return len(untagged), nil
}
