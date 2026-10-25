package deployment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

// What a deployment owns (design/subsystems/deployment.md, "Removing a
// deployment"):
// each thing AppTown created for it at its target, recorded by the driver
// as soon as the provider confirmed the creation. Take-down deletes exactly
// this list. Something AppTown found, reused or adopted is never on it, so
// it is never deleted.

// ownedRecorder is the deploy.Owner a driver built for one deployment
// records its creations through.
type ownedRecorder struct {
	db         *database.DB
	deployment ids.ID
	now        func() time.Time // AppTown's clock
}

func (r ownedRecorder) Created(ctx context.Context, o deploy.Owned) error {
	return recordOwned(ctx, r.db, r.deployment, o, "", clock.Or(r.now)())
}

// recordOwned adds one entry; the same thing recorded again is one entry.
// note is "" for a creation a driver saw; an entry recorded later says who
// approved it.
func recordOwned(ctx context.Context, db *database.DB, deployment ids.ID, o deploy.Owned, note string, at time.Time) error {
	if deployment.IsZero() || o.Kind == "" || o.ID == "" {
		return errors.New("an owned thing needs its deployment, kind and identity")
	}
	_, err := deploymentsql.New(db.SQL()).RecordOwned(ctx, deploymentsql.RecordOwnedParams{ID: ids.New(at.UTC()),
		DeploymentID: deployment, Kind: o.Kind, Ident: o.ID, Name: o.Name, HoldsData: o.Data, Need: o.Need,
		RecordedAt: at.UTC(), Note: note,
	})
	return err
}

// OwnerOf is what a driver built for dep records its creations through.
func (s *Deployments) OwnerOf(dep *Deployment) deploy.Owner {
	return ownedRecorder{db: s.db, deployment: dep.ID, now: s.clock}
}

// Owned is the deployment's list, oldest first.
func (s *Deployments) Owned(ctx context.Context, deployment ids.ID) ([]deploy.Owned, error) {
	return ownedIn(ctx, deploymentsql.New(s.db.SQL()), deployment)
}

func ownedIn(ctx context.Context, q *deploymentsql.Queries, deployment ids.ID) ([]deploy.Owned, error) {
	rows, err := q.OwnedByDeployment(ctx, deployment)
	if err != nil {
		return nil, err
	}
	out := make([]deploy.Owned, 0, len(rows))
	for _, r := range rows {
		out = append(out, deploy.Owned{Kind: r.Kind, ID: r.Ident, Name: r.Name, Data: r.HoldsData, Need: r.Need})
	}
	return out, nil
}

// repositoryRecorder is the digitalocean.RepositoryOwner for one project on
// one connector (one account): the repositories AppTown created there.
type repositoryRecorder struct {
	db         *database.DB
	project    ids.ID
	connector  string
	connection ids.ID
	now        func() time.Time // AppTown's clock
}

func (r repositoryRecorder) Owns(ctx context.Context, registry, repository string) (bool, error) {
	return deploymentsql.New(r.db.SQL()).RepositoryOwned(ctx, deploymentsql.RepositoryOwnedParams{ProjectID: r.project, ConnectionID: r.connection, Registry: registry, Repository: repository})
}

func (r repositoryRecorder) Created(ctx context.Context, registry, repository string) error {
	return recordRepository(ctx, r.db, r.project, r.connection, r.connector, registry, repository, "", clock.Or(r.now)())
}

func recordRepository(ctx context.Context, db *database.DB, project, connection ids.ID, connector, registry, repository, note string, at time.Time) error {
	if project.IsZero() || connection.IsZero() || registry == "" || repository == "" {
		return errors.New("a repository needs its project, registry and name")
	}
	_, err := deploymentsql.New(db.SQL()).RecordRepository(ctx, deploymentsql.RecordRepositoryParams{ID: ids.New(at.UTC()),
		ProjectID: project, ConnectionID: connection, Connector: connector, Registry: registry, Repository: repository, RecordedAt: at.UTC(), Note: note,
	})
	return err
}

// holdings is the digitalocean.Holdings of one deployment: its owned list,
// read and written by the driver that makes its bucket and key.
type holdings struct {
	deployments *Deployments
	dep         *Deployment
}

func (h holdings) Owned(ctx context.Context) ([]deploy.Owned, error) {
	return h.deployments.Owned(ctx, h.dep.ID)
}

func (h holdings) Created(ctx context.Context, o deploy.Owned) error {
	return h.deployments.OwnerOf(h.dep).Created(ctx, o)
}

func (h holdings) Forget(ctx context.Context, kind, id string) error {
	_, err := deploymentsql.New(h.deployments.db.SQL()).ForgetOwned(ctx, deploymentsql.ForgetOwnedParams{DeploymentID: h.dep.ID, Kind: kind, Ident: id})
	return err
}

// NeedSecretsScope is where a deployment's need credentials are kept in the
// built-in secret store (an object store's Spaces key secret), and what its
// generated local credentials are derived for (NeedCredentials).
func NeedSecretsScope(deployment ids.ID) string { return fmt.Sprintf("need:deployment:%s", deployment) }

// PreviewNeedScope is what a preview's generated need credentials are
// derived for, by the preview's name.
func PreviewNeedScope(preview string) string { return "need:preview:" + preview }

// NeedCredentials are the generated credentials of the needs a Docker host
// or Portainer runs for one owner (a deployment, NeedSecretsScope; a
// preview, PreviewNeedScope; one rehearsal), derived from the built-in
// store's key (secret.Vault.Derived): the same at every deploy, nothing
// kept, each owner's its own. Without a store there are none to give, and
// a need that generates one is refused rather than given the catalogue's.
func NeedCredentials(vault *secret.Vault, scope string) dependency.Credentials {
	return func(ctx context.Context, capability, name string) (string, error) {
		if vault == nil {
			return "", errors.New("this AppTown has no secret store to make it with")
		}
		return vault.Derived(ctx, scope, capability+"."+name)
	}
}

// needSecrets keeps one deployment's need credentials in the built-in store.
type needSecrets struct {
	vault *secret.Vault
	scope string
}

func (s needSecrets) Get(ctx context.Context, name string) (string, error) {
	v, _, err := s.vault.Get(ctx, s.scope, name)
	if errors.Is(err, secret.ErrNotStored) {
		return "", nil
	}
	return v, err
}

func (s needSecrets) Put(ctx context.Context, name, value string) error {
	_, err := s.vault.Put(ctx, s.scope, name, value, "apptown-system")
	return err
}

func (s needSecrets) Delete(ctx context.Context, name string) error {
	return s.vault.Delete(ctx, s.scope, name)
}
