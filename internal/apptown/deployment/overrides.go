package deployment

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// An operator's override:
// when a deployment needs more than its committed profile gives it now,
// before a change can commit more, an operator with the authority to
// deploy it sets a part's resources, with a reason. The override is a
// recorded act; it takes effect by redeploying the bundle the deployment
// runs, and holds over the profile at every deploy until an operator
// clears it or a deploy's committed profile declares the same.

// OverrideChange is an override to set or clear.
type OverrideChange struct {
	MemoryUnlimited bool
	ProjectID       ids.ID
	Deployment      string
	Part            string
	// Resources is what to set; zero clears unless MemoryUnlimited is set.
	Resources appcontract.Resources
	Reason    string
}

// OverrideResult is the recorded act and how it takes effect: the release
// that redeploys the running bundle, or why none was requested (nothing
// runs yet, or the request was refused; the override then applies at the
// next deploy).
type OverrideResult struct {
	Override *Override       `json:"override,omitempty"` // nil when cleared
	Release  *RedeployNumber `json:"release,omitempty"`
	NotYet   string          `json:"notYet,omitempty"`
}

// RedeployNumber names the release that redeploys the running bundle.
type RedeployNumber struct {
	Number     int64  `json:"number"`
	Deployment string `json:"deployment"`
}

var errOverrideReason = fault.New(fault.Invalid, "an override needs a reason")

// SetOverride records an operator's override of one part's resources, or
// clears it when in.Resources is zero, then redeploys the bundle the
// deployment runs so it takes effect now. The authority is the deploy
// grant, and the release grant for a protected deployment, as for any
// deploy. The part must be one the deployment's bundle declares.
func (s *Service) SetOverride(ctx context.Context, in OverrideChange) (OverrideResult, error) {
	if err := ctx.Err(); err != nil {
		return OverrideResult{}, err
	}
	if s.release.Authority == nil {
		return OverrideResult{}, errors.New("release operations are not configured")
	}
	dep, err := s.Deployments.ByName(ctx, in.ProjectID, in.Deployment)
	found, err := database.Lookup(err)
	if err != nil {
		return OverrideResult{}, err
	}
	directLimits := false
	if found {
		t, err := s.Targets.ByID(ctx, dep.TargetID)
		if err != nil {
			return OverrideResult{}, err
		}
		directLimits = DescribeTarget(t).ContainerMemoryLimits
	}
	caller, err := s.requireDeploy(ctx, in.ProjectID, in.Deployment, found && dep.Protected(), "deployment.override")
	if err != nil {
		return OverrideResult{}, err
	}
	if !found {
		return OverrideResult{}, ErrDeploymentMissing
	}
	if in.MemoryUnlimited && !directLimits {
		return OverrideResult{}, fault.New(fault.Invalid, "memory: none requires a Docker host or Portainer target; provider-managed sizes stay finite")
	}
	if in.MemoryUnlimited && in.Resources.MemoryBytes != 0 {
		return OverrideResult{}, fault.New(fault.Invalid, "unlimited memory and a positive memory override cannot both be selected")
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return OverrideResult{}, errOverrideReason
	}
	running, err := s.runningBundle(ctx, dep)
	if err != nil {
		return OverrideResult{}, err
	}
	if err := s.checkOverridePart(ctx, dep, running, in.Part); err != nil {
		return OverrideResult{}, err
	}
	who := caller.Principal.Name
	now := s.now()
	previous, err := s.Overrides(ctx, dep.ID)
	if err != nil {
		return OverrideResult{}, err
	}
	var out OverrideResult
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		q := deploymentsql.New(tx.SQL())
		payload := map[string]any{"deployment": dep.Name, "part": in.Part, "reason": reason, "memoryUnlimited": in.MemoryUnlimited}
		if old, ok := previous[in.Part]; ok {
			payload["previous"] = map[string]string{"memory": old.Resources.MemoryWords(), "cpu": old.Resources.CPUWords()}
		}
		if !in.Resources.Declared() && !in.MemoryUnlimited {
			n, err := q.DeleteDeploymentOverride(ctx, deploymentsql.DeleteDeploymentOverrideParams{DeploymentID: dep.ID, Part: in.Part})
			if err != nil {
				return err
			}
			if n == 0 {
				return fault.New(fault.NotFound, fmt.Sprintf("part %s of %s has no override", in.Part, dep.Name))
			}
			return audit.At(now).RecordIn(ctx, tx, who, dep.ProjectID, "deployment.resources-override-cleared", dep.Name, payload)
		}
		actor, err := identity.ActorIn(ctx, tx, who)
		if err != nil {
			return err
		}
		if err := q.PutDeploymentOverride(ctx, deploymentsql.PutDeploymentOverrideParams{DeploymentID: dep.ID, Part: in.Part,
			MemoryBytes: in.Resources.MemoryBytes, MemoryUnlimited: in.MemoryUnlimited, Cpus: in.Resources.CPUs, Reason: reason,
			SetBy: who, SetByPrincipal: actor.Principal, SetByVia: actor.Via, SetAt: now}); err != nil {
			return err
		}
		payload["memory"], payload["cpu"] = in.Resources.MemoryWords(), in.Resources.CPUWords()
		out.Override = &Override{MemoryUnlimited: in.MemoryUnlimited, Part: in.Part, Resources: in.Resources, Reason: reason, SetBy: who, SetAt: now.UTC().Format("2006-01-02T15:04:05Z")}
		return audit.At(now).RecordIn(ctx, tx, who, dep.ProjectID, "deployment.resources-overridden", dep.Name, payload)
	})
	if err != nil {
		return OverrideResult{}, err
	}
	if running.IsZero() {
		out.NotYet = "the running software could not be established; the override is saved for the next deployment, and no redeploy was started"
		return out, nil
	}
	rel, err := s.RequestRelease(ctx, DeploySelection{ProjectID: in.ProjectID, DeploymentName: dep.Name, BundleID: running})
	if err != nil {
		out.NotYet = fmt.Sprintf("the redeploy of the running bundle was not started (%v): the override applies at the next deploy", err)
		return out, nil
	}
	out.Release = &RedeployNumber{Number: rel.Number, Deployment: rel.Deployment}
	return out, nil
}

// checkOverridePart refuses a part or need the deployment's bundle does
// not declare: the running one's, or, when nothing runs, the project's latest.
func (s *Service) checkOverridePart(ctx context.Context, dep *Deployment, running ids.ID, part string) error {
	var bundle *ReleaseBundle
	var err error
	if !running.IsZero() {
		bundle, err = s.Bundles.ByID(ctx, running)
	} else {
		bundle, err = s.Bundles.Latest(ctx, dep.ProjectID)
	}
	if found, e := database.Lookup(err); e != nil {
		return e
	} else if !found || bundle == nil {
		return errNoBundle
	}
	project, err := s.release.Sources.Project(ctx, dep.ProjectID)
	if err != nil {
		return err
	}
	c, _, err := s.release.Sources.Contract(ctx, project, bundle.ContractRevision)
	if err != nil {
		return &contractReadError{err}
	}
	if c == nil {
		return &contractReadError{errors.New("the bundle has no committed contract")}
	}
	for _, p := range c.Parts {
		if p.Name == part {
			return nil
		}
	}
	// A need's service is sized as a part is, and overridden the same way.
	if slices.Contains(c.Needs, part) {
		return nil
	}
	return fault.New(fault.Invalid, fmt.Sprintf("the deployment's app has no part or need %q", part))
}

// clearCommittedOverrides clears each override the deploy just applied
// that its committed profile now declares itself: the change that
// committed the values has made the override redundant. Recorded as the
// AppTown's act, naming the revision. A failure leaves the override, which
// still holds the same values, and is said in the release's log.
func (s *Service) clearCommittedOverrides(ctx context.Context, dep *Deployment, committed *appcontract.Contract, applied AppliedProfile, revision string, logf func(string, ...any)) {
	if committed == nil || !applied.Recorded() {
		return
	}
	profiled := committed.ForProfile(applied.Profile).PartResources()
	for part, p := range applied.Parts {
		if p.MemoryUnlimited || !p.Override.Declared() || !covers(profiled[part], p.Override) {
			continue
		}
		err := s.db.InTx(ctx, func(tx *database.Tx) error {
			n, err := deploymentsql.New(tx.SQL()).DeleteDeploymentOverride(ctx, deploymentsql.DeleteDeploymentOverrideParams{DeploymentID: dep.ID, Part: part})
			if err != nil || n == 0 {
				return err
			}
			return audit.At(s.now()).RecordIn(ctx, tx, "apptown-system", dep.ProjectID, "deployment.resources-override-cleared", dep.Name,
				map[string]any{"deployment": dep.Name, "part": part, "reason": "the committed profile now matches", "revision": revision})
		})
		if err != nil {
			logf("part %s's override is now committed but could not be cleared: %v", part, err)
			continue
		}
		logf("part %s's override is cleared: the committed profile %s now declares the same", part, applied.Profile)
	}
}

// covers says whether declared sets every field override sets, to the
// same value.
func covers(declared, override appcontract.Resources) bool {
	if override.MemoryBytes > 0 && declared.MemoryBytes != override.MemoryBytes {
		return false
	}
	if override.CPUs > 0 && declared.CPUs != override.CPUs {
		return false
	}
	return true
}
