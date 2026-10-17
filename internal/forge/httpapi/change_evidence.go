package httpapi

import (
	"context"
	"encoding/json"

	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/workflow"
)

// A compact read model made from receipts, separate from the generated narrative.
type changeEvidence struct {
	Targets  []workflow.AcceptanceCheck `json:"targets"`
	Merged   bool                       `json:"merged"`
	Releases []changeRelease            `json:"releases"`
}

type changeRelease struct {
	Number     int64                   `json:"number"`
	Deployment string                  `json:"deployment"`
	State      deployment.ReleaseState `json:"state"`
	Serving    bool                    `json:"serving"`
	// Observed is what the deployment serving this release's record was
	// observed to be (deployment.ContainerPresence): running, stopped,
	// missing, or unavailable/unknown with Reason. Only running is serving;
	// "" when no deployment records serving it.
	Observed string `json:"observed,omitempty"`
	Reason   string `json:"reason,omitempty"`
	release  *deployment.Release
}

func (h ChangeDetail) evidenceForChange(ctx context.Context, ch *workflow.Change) (changeEvidence, error) {
	out := changeEvidence{Targets: []workflow.AcceptanceCheck{}, Releases: []changeRelease{}}
	var acc workflow.Acceptance
	if json.Unmarshal([]byte(ch.AcceptanceJSON), &acc) == nil && len(acc.Checks) > 0 {
		out.Targets = acc.Checks
	}
	var err error
	out.Merged, err = workflow.ChangeMergedIn(ctx, h.db, ch.ProjectID, ch.ID)
	if err != nil {
		return out, err
	}
	carried, err := h.deployment.Releases.ForChange(ctx, ch.ProjectID, ch.ID)
	if err != nil {
		return out, err
	}
	for _, r := range carried {
		out.Releases = append(out.Releases, changeRelease{Number: r.Number, Deployment: r.Deployment, State: r.State, release: r})
	}
	if len(out.Releases) > 0 {
		deployments, err := h.deployment.Deployments.ForProject(ctx, ch.ProjectID)
		if err != nil {
			return out, err
		}
		for _, env := range deployments {
			matches := []int{}
			// The release this deployment serves is its live one, by the
			// owner's rule the release list and the release's page read
			// (Releases.Live): a promotion has no landing run, and an App
			// Platform deployment records no commit.
			for i, rel := range out.Releases {
				if rel.Deployment != env.Name || (!rel.release.DeploymentID.IsZero() && rel.release.DeploymentID != env.ID) {
					continue
				}
				live, err := h.deployment.Releases.Live(ctx, rel.release)
				if err != nil {
					return out, err
				}
				if live {
					matches = append(matches, i)
				}
			}
			if len(matches) == 0 {
				continue
			}
			// Serving is a positive observation by the deployment's own
			// target. Anything the owner could not observe stays unknown
			// (or unavailable), never "not serving".
			o := h.deployment.ObserveContainer(ctx, env)
			for _, i := range matches {
				out.Releases[i].Serving = o.State == deployment.ContainerRunning
				out.Releases[i].Observed, out.Releases[i].Reason = o.State.String(), o.Reason
			}
		}
	}
	return out, nil
}
