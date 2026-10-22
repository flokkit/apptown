package deployment

import (
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// RefusesContract is the sentence for a contract the target cannot express
// (§4.1 step 1; healthchecks.md §4.4's refusal, generalised), or "": an
// app that keeps data in a directory needs persistent volumes, and an app
// whose needs a managed-needs target cannot fulfil cannot stand there.
func (t *Target) RefusesContract(c *appcontract.Contract) string {
	if t.Kind == KindS3Bucket && !StaticOnly(c) {
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
	if t.NeedsMode == "managed" {
		for _, n := range c.Needs {
			if !contains(t.Capabilities.Needs, n) {
				return fmt.Sprintf("this app needs %s, which %s cannot provide as a managed service", n, t.Name)
			}
		}
	}
	return ""
}

// StaticOnly says whether c is a static site and nothing else: one part,
// public and static, no init steps, no needs. A bucket can serve only that.
func StaticOnly(c *appcontract.Contract) bool {
	return c != nil && len(c.Parts) == 1 && c.Parts[0].Public && c.Parts[0].IsStatic() && len(c.Init) == 0 && len(c.Needs) == 0
}

// refusesEarlierStaticImage is the sentence for a static site's bundle of
// the earlier form (its image holds only the files: BundleOrigin.SelfServing
// unset) bound for App Platform, which runs an image as it is and would
// serve the file server's default page; or "". A Docker host and Portainer
// run such a bundle with the file server's command, as they always did.
func refusesEarlierStaticImage(kind string, b *ReleaseBundle, c *appcontract.Contract) string {
	if kind != KindDOApp || b == nil || b.BuiltFrom.SelfServing {
		return ""
	}
	for _, p := range c.Parts {
		if p.Static != "" || p.StaticBuild != nil {
			return fmt.Sprintf("bundle %d's static site image (part %s) was built before static images served themselves (3 October 2026), and App Platform runs an image as it is; release again to get a self-serving image", b.ID, p.Name)
		}
	}
	return ""
}

// refusal is a target's refusal sentence as an error, or nil for none.
func refusal(why string) error {
	if why == "" {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrRefusedContract, why)
}

var (
	ErrPreviewNotDeployed = fault.New(fault.Conflict, "a preview is not deployed to; it follows its change")
	ErrFedByLanding       = fault.New(fault.Conflict, "fed by landing: every landing deploys it")
)

var ErrMainNotRemoved = fault.New(fault.Conflict, "main is the deployment every landing feeds; it cannot be removed")

// Removable says whether a deployment may be torn down on purpose: main is
// the deployment every landing feeds, so it is never removed by hand.
func (e *Deployment) Removable() error {
	if e.Name == "main" {
		return ErrMainNotRemoved
	}
	return nil
}

// DeployableByHand says whether someone may deploy to this deployment on
// purpose: a preview follows its change, and a landing-fed deployment is
// deployed by every landing, so neither takes a deploy by hand.
func (e *Deployment) DeployableByHand() error {
	if !e.Standing() {
		return ErrPreviewNotDeployed
	}
	if e.Feed == "landing" {
		return fmt.Errorf("%s is %w", e.Name, ErrFedByLanding)
	}
	return nil
}

// DeployableByHandAt is DeployableByHand for a deploy of bundle, given the
// bundle the deployment is known to run (0: not known, see runningBundle). A
// landing-fed deployment takes by hand only that bundle: deploying what runs
// again changes nothing a landing would choose, and it records the release's
// rebuild inputs, which a release made before they were recorded lacks
// (stock/docs/guides/apptown-backups.md).
func (e *Deployment) DeployableByHandAt(bundle, running ids.ID) error {
	err := e.DeployableByHand()
	if errors.Is(err, ErrFedByLanding) && !running.IsZero() && bundle == running {
		return nil
	}
	return err
}
