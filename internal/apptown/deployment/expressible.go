package deployment

import (
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/fly"
	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// refusesContract is the sentence for a contract the target cannot express
// (§4.1 step 1; healthchecks.md §4.4's refusal, generalised), or "": an
// app that keeps data in a directory needs persistent volumes, and an app
// whose needs a managed-needs target cannot fulfil cannot stand there.
func (t *Target) refusesContract(c *appcontract.Contract) string {
	var healthErr error
	switch t.Kind {
	case KindDockerHost:
		healthErr = docker.RefusesHealth(c)
	case KindPortainer:
		healthErr = portainer.RefusesHealth(c)
	case kindFlyMachines:
		healthErr = fly.RefusesHealth(c)
	case KindS3Bucket:
		for _, part := range c.Parts {
			if part.Intent == nil {
				continue
			}
			for purpose, probe := range map[string]*appcontract.Probe{"readiness": part.Intent.Readiness, "liveness": part.Intent.Liveness} {
				p, err := appcontract.NormalizeHealthPolicy(probe, purpose, appcontract.HealthPolicy{Period: time.Second, Timeout: time.Second, FailureThreshold: 1, SuccessThreshold: 1})
				if err != nil {
					return err.Error()
				}
				if p.Explicit {
					return "a static bucket has no runtime health controller for declared execution policy"
				}
			}
		}
	}
	if healthErr != nil {
		return healthErr.Error()
	}
	if t.Kind == KindS3Bucket && !staticOnly(c) {
		return fmt.Sprintf("%s serves only a static-only app: one public static part, no init steps and no needs — this app needs a container target", t.Name)
	}
	if !t.Capabilities.PersistentVolumes {
		if c.PublicPart().DataDir != "" {
			return fmt.Sprintf("this app keeps data in a directory (%s); %s has no persistent volumes — keep the data in a need such as postgres instead, or release to a Docker host", c.PublicPart().DataDir, t.Name)
		}
		for _, p := range c.Parts {
			if p.DataDir != "" {
				return fmt.Sprintf("part %s keeps data in a directory (%s); %s has no persistent volumes — keep the data in a need such as postgres instead, or release to a Docker host", p.Name, p.DataDir, t.Name)
			}
		}
	}
	if t.Kind == kindFlyMachines {
		for _, n := range c.Needs {
			if !contains(t.Capabilities.Needs, n) {
				return fmt.Sprintf("this app needs %s, which %s cannot run as a need container", n, t.Name)
			}
		}
	}
	if t.NeedsMode == "managed" {
		for _, n := range c.Needs {
			if !contains(t.Capabilities.Needs, n) {
				return fmt.Sprintf("this app needs %s, which %s cannot provide as a managed service", n, t.Name)
			}
		}
	}
	return ""
}

// staticOnly says whether c is a static site and nothing else: one part,
// public and static, no init steps, no needs. A bucket can serve only that.
func staticOnly(c *appcontract.Contract) bool {
	return c != nil && len(c.Parts) == 1 && c.Parts[0].Public && c.Parts[0].IsStatic() && len(c.Init) == 0 && len(c.Needs) == 0
}

// refusal is a target's refusal sentence as an error, or nil for none.
// refusesAudience is the target's refusal of dep's audience, or "".
// A Fly app answers anyone at its fly.dev address and AppTown puts no
// sign-in in front of it, so only a public deployment says what Fly does.
// The next step is one the person can take: a deployment that has run
// keeps its audience (making it public is a migration EnsureStanding
// refuses), so for that one the step is a new public deployment.
func (t *Target) refusesAudience(dep *Deployment) string {
	if t.Kind != kindFlyMachines || dep.Audience == "public" {
		return ""
	}
	next := "set this deployment's audience to public"
	if dep.DesiredGeneration != 0 || dep.ObservedGeneration != 0 {
		next = fmt.Sprintf("this deployment has run, so its audience cannot change; make a new public deployment of this project on %s", t.Name)
	}
	return fmt.Sprintf("%s serves the app at its fly.dev address to anyone, with no AppTown sign-in in front of it, so it cannot keep a deployment to the %s audience; %s, or deploy it to a Docker host", t.Name, dep.Audience, next)
}

func refusal(why string) error {
	if why == "" {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrRefusedContract, why)
}

var (
	errPreviewNotDeployed = fault.New(fault.Conflict, "a preview is not deployed to; it follows its change")
	errFedByLanding       = fault.New(fault.Conflict, "fed by landing: every landing deploys it")
)

var errMainNotRemoved = fault.New(fault.Conflict, "main is the deployment every landing feeds; it cannot be removed")

// removable says whether a deployment may be torn down on purpose: main is
// the deployment every landing feeds, so it is never removed by hand.
func (e *Deployment) removable() error {
	if e.Feed == "main" {
		return errMainNotRemoved
	}
	return nil
}

// deployableByHand says whether someone may deploy to this deployment on
// purpose: a preview follows its change, and a landing-fed deployment is
// deployed by every landing, so neither takes a deploy by hand.
func (e *Deployment) deployableByHand() error {
	if !e.Standing() {
		return errPreviewNotDeployed
	}
	if e.Feed == "main" {
		return fmt.Errorf("%s is %w", e.Name, errFedByLanding)
	}
	return nil
}

// deployableByHandAt permits an explicit project-owned bundle on follows-main.
// A request for latest still belongs to the automatic feed. Previews never
// accept a release by hand.
func (e *Deployment) deployableByHandAt(bundle, running ids.ID) error {
	err := e.deployableByHand()
	if errors.Is(err, errFedByLanding) && !bundle.IsZero() {
		return nil
	}
	return err
}
