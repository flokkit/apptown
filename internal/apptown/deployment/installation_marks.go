package deployment

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/deploy/s3bucket"
	"github.com/flokkit/apptown/internal/ids"
)

// MarkInstallationJob marks, in place, what deployment records name and
// AppTown made before it marked what it makes with its installation id.
const MarkInstallationJob = "installation-marks"

// MarkRecorded gives every resource a deployment record names the
// installation's mark where the provider allows it in place: a managed
// database cluster its tag, a bucket website's bucket its private mark.
// Untagged resources stay accounted for by their records meanwhile; the
// mark keeps them accounted for once their records go. Docker labels are
// fixed at creation and a Portainer stack's variables or an App Platform
// spec change only with a redeploy, so those gain the mark at their next
// release instead. Each step is idempotent, so a second pass marks nothing;
// a pass that marked anything records how many in this pass.
func (p *Providers) MarkRecorded(ctx context.Context) (int, error) {
	if p.installation == "" {
		return 0, nil
	}
	targets, err := p.targets.All(ctx)
	if err != nil {
		return 0, err
	}
	marked := 0
	var failed []error
	for _, t := range targets {
		mark, ok := p.markers[t.Kind]
		if !ok || t.Status == TargetRetired {
			continue
		}
		deps, err := p.deployments.OnTarget(ctx, t.ID)
		if err != nil {
			failed = append(failed, err)
			continue
		}
		n, err := mark(ctx, t, deps)
		marked += n
		if err != nil {
			failed = append(failed, fmt.Errorf("marking what %s holds: %w", t.Name, err))
		}
	}
	if marked > 0 {
		at := audit.At(clock.Or(p.clock)())
		if err := p.db.InTx(ctx, func(tx *database.Tx) error {
			return at.RecordIn(ctx, tx, "apptown-system", ids.Nil, "deployment.resources-marked", "installation",
				map[string]any{"marked": marked, "installation": p.installation, "meaning": "resources a deployment records, marked in this pass as this installation's"})
		}); err != nil {
			failed = append(failed, err)
		}
	}
	return marked, errors.Join(failed...)
}

// markerFactory marks, in place, what the deployments on one target record;
// it answers how many it marked. A kind registers one only when its provider
// can mark an existing resource without redeploying it.
type markerFactory func(ctx context.Context, t *Target, deps []ProjectDeployment) (int, error)

// markClusters tags the managed database clusters App Platform deployments
// record.
func (p *Providers) markClusters(ctx context.Context, t *Target, deps []ProjectDeployment) (int, error) {
	var clusters []string
	for _, d := range deps {
		insts, err := p.needs.ForDeployment(ctx, d.ID)
		if err != nil {
			return 0, err
		}
		for _, n := range insts {
			if id := databaseResource(n.Handle).ID; n.Mode == "managed" && id != "" {
				clusters = append(clusters, id)
			}
		}
	}
	if len(clusters) == 0 {
		return 0, nil
	}
	c, _, err := p.cloudClientFor(t.Provider)(ctx)
	if err != nil {
		return 0, err
	}
	return digitalocean.MarkClusters(ctx, c, p.installation, clusters)
}

// markBuckets writes the private mark into the buckets bucket-website
// deployments record.
func (p *Providers) markBuckets(ctx context.Context, t *Target, deps []ProjectDeployment) (int, error) {
	c, _, err := p.s3BucketClientFor(t.ID)(ctx)
	if err != nil {
		return 0, err
	}
	marked := 0
	for _, d := range deps {
		bucket, _ := d.Handle["bucket"].(string)
		if bucket == "" {
			continue
		}
		have, err := s3bucket.Marked(ctx, c, bucket)
		if s3bucket.IsCode(err, "NoSuchBucket") {
			continue // gone: nothing to mark, and its record reads missing
		}
		if err != nil {
			return marked, err
		}
		if have == p.installation {
			continue
		}
		if err := s3bucket.MarkInstallation(ctx, c, bucket, p.installation); err != nil {
			return marked, fmt.Errorf("marking %s: %w", bucket, err)
		}
		marked++
	}
	return marked, nil
}
