package httpapi

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/gitexec"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/strs"
)

// ShippedComparison is what a change with no code of its own (Work's
// CarriesNoCode: only landing runs, the ship workflow invoked on it) put
// live: main as it stood, as a release. Since is the earlier release it was
// compared with, set only when that comparison was made; the change's diff
// is then what this release put live that that one did not.
type ShippedComparison struct {
	Deployment     string `json:"deployment,omitempty"`
	Release        int64  `json:"release,omitempty"`
	Commit         string `json:"commit,omitempty"`
	Previous       int64  `json:"previous,omitempty"`
	PreviousCommit string `json:"previousCommit,omitempty"`
	Since          int64  `json:"since,omitempty"`
}

// shippedComparison describes a change that carries no code: the comparison
// the change page shows, and the diff when one was read (ok). A read that
// failed is said in the message, never shown as an empty change.
func shippedComparison(ctx context.Context, releases *deployment.Releases, repo string, ch *workflow.Change) (cmp CommittedComparison, diff scm.CommittedDiff, ok bool) {
	cmp = CommittedComparison{Status: "no-code"}
	lead := "This change has no code of its own"
	shipped, err := releases.ShippedBy(ctx, ch.ProjectID, ch.ID)
	if err != nil {
		cmp.Message = fmt.Sprintf("%s; what it shipped could not be read: %v", lead, err)
		return cmp, diff, false
	}
	if shipped == nil {
		cmp.Message = lead + ": it shipped main as it stood, and no release carrying it is recorded as released."
		return cmp, diff, false
	}
	cmp.Shipped = &ShippedComparison{Deployment: shipped.Deployment, Release: shipped.Number, Commit: shipped.Commit, Previous: shipped.Previous, PreviousCommit: shipped.PreviousCommit}
	what := fmt.Sprintf("release #%d to %s", shipped.Number, shipped.Deployment)
	if shipped.Commit == "" {
		cmp.Message = fmt.Sprintf("%s: it shipped main as %s, which recorded no commit, so what it put live cannot be shown.", lead, what)
		return cmp, diff, false
	}
	at := fmt.Sprintf("%s: it shipped main as it stood at %s, as %s", lead, strs.ShortSHA(shipped.Commit), what)
	switch {
	case shipped.Previous == 0:
		cmp.Message = at + ", the first release there, so there is nothing earlier to compare it with."
		return cmp, diff, false
	case shipped.PreviousCommit == "":
		cmp.Message = fmt.Sprintf("%s. Release #%d recorded no commit, so what changed since cannot be shown.", at, shipped.Previous)
		return cmp, diff, false
	}
	diff, err = scm.ReadCommitsDiff(ctx, repo, shipped.PreviousCommit, shipped.Commit)
	if err != nil {
		cmp.Message = fmt.Sprintf("%s. Comparing it with release #%d failed: %v", at, shipped.Previous, err)
		return cmp, diff, false
	}
	cmp.Shipped.Since = shipped.Previous
	if diff.Empty {
		cmp.Message = fmt.Sprintf("%s. It put live the same code as release #%d.", at, shipped.Previous)
	} else {
		cmp.Message = fmt.Sprintf("%s. Below is what release #%d put live that release #%d did not.", at, shipped.Number, shipped.Previous)
	}
	return cmp, diff, true
}

// branchPublished reports whether a change's branch exists in the project's
// repository. A shipped change with no code of its own never had one; one
// that exists holds the change's own commits and is shown as such.
func branchPublished(ctx context.Context, hub, branch string) bool {
	if branch == "" {
		return false
	}
	_, err := gitexec.Run(ctx, hub, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}
