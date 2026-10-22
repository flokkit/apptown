package deployment

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// Moving a deployment made before its names were by key to them
// (design/subsystems/deployment.md, "Names on a host"). A
// person asks for it (apptown move-names); it is a release of the bundle
// the deployment runs, admitted as any release, and the Docker driver
// moves the stack inside that release's attempt (docker/move.go).
//
// REMOVE-AFTER resource move: this file, the column release_requests.
// move_names and the door go once every deployment is moved.

// refusesMove says why dep cannot be moved to its names by key, "" when it
// can: a standing deployment on a Docker host, still on the names it was
// made with.
func refusesMove(dep *Deployment, t *Target) string {
	switch {
	case !dep.Standing():
		return dep.Name + " is not a standing deployment; only those have names of their own on a host"
	case dep.HostPrefix == "":
		return dep.Name + " is already named by its key"
	case t.DataAccess() == DataThroughPortainer:
		return "AppTown does not move a deployment on Portainer. Take it down and release it again to name it by its key; its data is not carried over"
	case t.DataAccess() != DataOnDockerHost:
		return dep.Name + " is on " + t.Name + ", which names nothing on a Docker host"
	}
	return ""
}

// errNothingToMove refuses a move of a deployment with nothing released:
// a failed first release may have left data on the host, which only a
// release of what it runs can find and copy.
var errNothingToMove = fault.New(fault.Unprocessable, "it has nothing released to move: release it first, then move it")

// SwitchToKeyNames records, in one transaction, that a deployment is now
// named by its key: no prefix, its UUID as its label on the host, the
// containers, networks and volumes recorded under its old names forgotten
// (the old volumes stay on the host, outside AppTown's records), and made
// (the volumes the move copied into) recorded. Its image rows stay:
// take-down still deletes those build images.
func (s *Deployments) SwitchToKeyNames(ctx context.Context, id ids.ID, made []deploy.Owned) error {
	at := s.now()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := deploymentsql.New(tx.SQL())
		n, err := q.SwitchToKeyNames(ctx, deploymentsql.SwitchToKeyNamesParams{ID: id, UpdatedAt: at})
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("the deployment is no longer on the names it was made with")
		}
		if _, err := q.ForgetHostResources(ctx, id); err != nil {
			return err
		}
		for _, o := range made {
			if o.Kind != "volume" || o.ID == "" {
				return fmt.Errorf("the move records only the volumes it made, not %s", o.Label())
			}
			if _, err := q.RecordOwned(ctx, deploymentsql.RecordOwnedParams{ID: ids.New(at), DeploymentID: id, Kind: o.Kind, Ident: o.ID,
				Name: o.Name, HoldsData: o.Data, Need: o.Need, RecordedAt: at}); err != nil {
				return err
			}
		}
		return nil
	})
}

// moveDriver gives the Docker driver of a moving release the owner's
// switch.
func (s *Service) moveDriver(d deploy.Driver, dep *Deployment) error {
	dd, ok := d.(*docker.Driver)
	if !ok {
		return errors.New("only a deployment on a Docker host is moved to its names by key")
	}
	dd.Move = &docker.Move{Switch: func(ctx context.Context, made []deploy.Owned) error {
		return s.Deployments.SwitchToKeyNames(ctx, dep.ID, made)
	}}
	return nil
}
