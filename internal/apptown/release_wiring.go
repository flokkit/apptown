package apptown

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

type releaseSecrets struct{ secrets *secret.Service }

func (d releaseSecrets) ResolveRelease(ctx context.Context, p deployment.Application, name, staged string, c *appcontract.Contract) (deploy.ConsumerSecrets, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	delivery, err := d.secrets.Resolve(ctx, p.ID, p.Slug, name, staged, c)
	if err != nil {
		return nil, nil, err
	}
	var names []string
	for _, r := range delivery.Receipts {
		names = append(names, r.Name)
	}
	return delivery, names, ctx.Err()
}

// ResolveRestoredCopy gives a restored copy of a deployment only what the
// rehearsal profile names, against the source's bindings
// (secret.ResolveRehearsal): the same rule as a restore rehearsal.
func (d releaseSecrets) ResolveRestoredCopy(ctx context.Context, p deployment.Application, source, staged string, c *appcontract.Contract) (deploy.ConsumerSecrets, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	got, err := d.secrets.ResolveRehearsal(ctx, p.ID, source, staged, c)
	if err != nil {
		return nil, got.Withheld, err
	}
	return got.Delivery, got.Withheld, ctx.Err()
}

type releaseJobs struct{}

func (releaseJobs) EnqueueReleaseIn(ctx context.Context, tx *database.Tx, id ids.ID) error {
	return jobQueue{}.EnqueueIn(ctx, tx, jobs.Request{Operation: deployment.ReleaseJob, Key: id.String()})
}
