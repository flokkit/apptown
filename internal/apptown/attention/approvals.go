package attention

import (
	"context"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/clock"
)

// Approvals lists the releases waiting for the caller's approval as a source
// of its own, so All checks counts them under a row like every other
// condition. The deployment owner decides which waiting releases the caller
// may approve; this source shows that answer and no instance-wide view.
type Approvals struct {
	Releases interface {
		WaitingReleases(context.Context) ([]deployment.WaitingRelease, error)
	}
	Now func() time.Time
}

func (a Approvals) Snapshot(ctx context.Context) (Snapshot, error) {
	waiting, err := a.Releases.WaitingReleases(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	out := Snapshot{Known: true, ObservedAt: clock.Or(a.Now)(), Href: "/attention", NextStep: "Open the release to approve or refuse it."}
	for _, task := range waiting {
		at, err := time.Parse(time.RFC3339Nano, task.Release.CreatedAt)
		if err != nil {
			return Snapshot{}, err
		}
		out.Items = append(out.Items, Item{StatusLabel: labelApproval, Severity: SeverityWarning, Key: task.Release.ID.String(), Title: fmt.Sprintf("Release %d to %s needs your approval", task.Release.Number, task.DeploymentLabel), Detail: task.Project, Href: task.Href, ObservedAt: at})
	}
	return out, nil
}
