package deployment

import (
	"context"
	"database/sql"
	"errors"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/database"
)

// Shipped is what a change's newest released release put live, and what the
// release before it on the same deployment had: the commit each was built
// from. Previous is zero when no earlier release on that deployment was
// released. A commit is "" when its release recorded none (a release from
// before releases recorded their source), and the caller says it is unknown.
type Shipped struct {
	Deployment     string
	Number         int64
	Commit         string
	Previous       int64
	PreviousCommit string
}

// ShippedBy is what the change's newest released release shipped; nil when
// no release carrying the change was released.
func (s *Releases) ShippedBy(ctx context.Context, projectID, changeID ids.ID) (*Shipped, error) {
	carried, err := s.ForChange(ctx, projectID, changeID)
	if err != nil {
		return nil, err
	}
	var rel *Release
	for _, r := range carried { // newest first
		if r.State == Released {
			rel = r
			break
		}
	}
	if rel == nil {
		return nil, nil
	}
	q := deploymentsql.New(s.db.SQL())
	out := &Shipped{Deployment: rel.Deployment, Number: rel.Number}
	if out.Commit, err = shippedCommit(ctx, q, rel); err != nil {
		return nil, err
	}
	before, err := releaseRecord(q.ReleasedBefore(ctx, deploymentsql.ReleasedBeforeParams{ProjectID: projectID, Deployment: rel.Deployment, State: Released.String(), Number: rel.Number}))
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, database.QueryError(err, "reading the release before")
	}
	out.Previous = before.Number
	if out.PreviousCommit, err = shippedCommit(ctx, q, before); err != nil {
		return nil, err
	}
	return out, nil
}

// shippedCommit is the commit a release was built from: its bundle's, else
// the source its image was built from.
func shippedCommit(ctx context.Context, q *deploymentsql.Queries, rel *Release) (string, error) {
	if rel.BundleID.IsZero() {
		return rel.SourceSHA, nil
	}
	b, err := q.BundleOfRelease(ctx, rel.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return rel.SourceSHA, nil
	}
	if err != nil {
		return "", database.QueryError(err, "reading a release's bundle")
	}
	return b.ContractRevision, nil
}
