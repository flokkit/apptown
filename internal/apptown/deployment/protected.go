package deployment

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// ProtectedSettings compares the stored value with the value the person edited.
type ProtectedSettings struct {
	Protected bool   `json:"protected"`
	Was       bool   `json:"was"`
	Meaning   string `json:"meaning,omitempty"`
}

func unprotectMeaning(label string) string {
	return fmt.Sprintf("I state releases to %s need no sign-off", label)
}

// SetProtected changes the release setting and records personal assent when
// removing it. Classification only supplies the starting value.
func (s *Service) SetProtected(ctx context.Context, projectID ids.ID, name string, in ProtectedSettings) (*Deployment, error) {
	actor, err := s.requireDeploy(ctx, projectID, name, true, "deployment.protected")
	if err != nil {
		return nil, err
	}
	dep, err := s.Deployments.ByName(ctx, projectID, name)
	if err != nil {
		return nil, err
	}
	if !dep.Standing() {
		return nil, fault.New(fault.Invalid, "only a standing deployment has release protection to change")
	}
	if !in.Protected {
		if !actor.Verified || actor.Principal.Kind != "user" || !actor.Principal.Actor().Assents() {
			return nil, fault.New(fault.Forbidden, "turning protection off needs a person's own assent")
		}
		if in.Meaning != unprotectMeaning(dep.DisplayLabel) {
			return nil, fault.New(fault.Unprocessable, "confirm: "+unprotectMeaning(dep.DisplayLabel))
		}
	}
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		if err := LockProjectIn(ctx, tx, projectID); err != nil {
			return err
		}
		if err := project.RefuseUnlessActiveIn(ctx, tx, projectID); err != nil {
			return err
		}
		current, err := lockDeploymentRecordIn(ctx, tx, dep.ID)
		if err != nil {
			return err
		}
		if current.Protected != in.Was {
			return fault.New(fault.Conflict, "protection changed while you were editing; reload before saving")
		}
		if !in.Protected && in.Meaning != unprotectMeaning(current.DisplayLabel) {
			return fault.New(fault.Conflict, "the deployment label changed; reload before saving")
		}
		if current.Protected == in.Protected {
			return nil
		}
		n, err := deploymentsql.New(tx.SQL()).SetDeploymentProtected(ctx, deploymentsql.SetDeploymentProtectedParams{ID: dep.ID, Protected: in.Protected, Was: in.Was, At: s.now()})
		if err != nil {
			return err
		}
		if n != 1 {
			return fault.New(fault.Conflict, "protection changed; reload before saving")
		}
		kind := "deployment.protected"
		if !in.Protected {
			kind = "deployment.unprotected"
		}
		return audit.At(s.now()).RecordIn(ctx, tx, actor.Principal.Name, projectID, kind, name, in)
	})
	if err != nil {
		return nil, err
	}
	return s.Deployments.ByID(ctx, dep.ID)
}
