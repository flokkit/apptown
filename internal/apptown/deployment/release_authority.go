package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/ids"
)

var errReleaseAuthority = errors.New("this release has no complete requester identity; ask for a new release")

// A newly queued release never uses the shared resolver's historical name
// fallback. The identity and credential must both be the ones admitted.
func releasePrincipal(raw json.RawMessage) (access.Principal, error) {
	var captured operations.Authority
	if len(raw) == 0 || json.Unmarshal(raw, &captured) != nil {
		return access.Principal{}, errReleaseAuthority
	}
	pr := captured.Principal()
	kind, ref, ok := strings.Cut(pr.Credential, ":")
	if !ok || ref == "" || pr.Name == "" {
		return access.Principal{}, errReleaseAuthority
	}
	if kind == "system" {
		if pr.Kind != "instrument" || pr.Instrument != access.SystemInstrument {
			return access.Principal{}, errReleaseAuthority
		}
	} else if pr.ID.IsZero() || pr.Kind != "user" {
		return access.Principal{}, errReleaseAuthority
	}
	return pr, nil
}

// Copy admission belongs to data protection. Execution keeps the same source
// and destination, and asks that owner to recheck both restore permissions.
type releaseCopyAuthority interface {
	CheckStartAuthority(context.Context, ids.ID, ids.ID, string, access.Principal) error
}

func (s *Service) requireReleaseAuthority(ctx context.Context, captured access.Principal, deploymentID ids.ID, admittedPromotion bool) error {
	if s.release.Still == nil || s.projects == nil {
		return errors.New("release authority recheck is not configured")
	}
	dep, err := s.Deployments.ByID(ctx, deploymentID)
	if err != nil {
		return err
	}
	resource, err := s.projects.AuthorityResource(ctx, dep.ProjectID)
	if err != nil {
		return err
	}
	permission := access.Permission{Capability: access.CapDeploymentDeploy, Resource: resource, Context: access.Context{Consequential: true, Route: "deployment.execute"}}
	if err := s.release.Still.RequireStill(ctx, captured, permission); err != nil {
		return err
	}
	if admittedPromotion || dep.needsPromotion() {
		permission.Capability = access.CapReleasePromote
		permission.Resource.Type, permission.Resource.ID = "release-target", dep.Name
		if err := s.release.Still.RequireStill(ctx, captured, permission); err != nil {
			return err
		}
	}
	if !dep.StartFrom.IsZero() && !dep.StartedFrom.Valid {
		starter, ok := s.starter.(releaseCopyAuthority)
		if !ok {
			return errors.New("copy-start authority recheck is not configured")
		}
		return starter.CheckStartAuthority(ctx, dep.StartFrom, dep.ProjectID, dep.DataClass, captured)
	}
	return nil
}
