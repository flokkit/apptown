package workflowexec

import (
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

// Release bundles (design/subsystems/deployment.md §2.8): the immutable thing that
// ships, cut once at landing from the images the deploy step booted.
// Every environment of the project runs from a bundle; promotion never
// chooses images, it chooses a bundle.

// cutBundle records what a landing built, read from the daemon it booted
// on, and names that target: the images exist there and nowhere else. The
// deploy step's attempt commits it, only while it still owns the step.
func (a *Shipping) cutBundle(task *workflow.Execution, projectID ids.ID, tree string, landed deployment.LandingResult, contractBytes []byte, from deployment.BundleOrigin) (ids.ID, error) {
	parts, init := landed.Images(task.Context())
	from.TargetID, from.By = landed.TargetID, task
	return a.deployment.Bundles.Cut(task.Context(), projectID, tree, parts, init, contractBytes, from)
}
