package verification

import (
	"context"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// Portfolio reads standing coverage and deployment facts for consent.
type Portfolio struct {
	Releases *deployment.Service
}

func (p Portfolio) StandingHold(ctx context.Context, q database.Queryer, pid ids.ID) (string, error) {
	holds, err := p.Releases.HeldBackIn(ctx, q, pid)
	if err != nil {
		return "", err
	}
	reasons := make([]string, 0, len(holds))
	for _, hold := range holds {
		reasons = append(reasons, hold.String())
	}
	return strings.Join(reasons, "; "), nil
}

// StandingReach reads repository eligibility without inspecting source or scans.
func (p Portfolio) StandingReach(ctx context.Context, q database.Queryer, pid ids.ID) (workflow.StandingReach, error) {
	item, err := project.ByIDIn(ctx, q, pid)
	if err != nil {
		return workflow.StandingReach{}, err
	}
	out := workflow.StandingReach{Repository: item.DefaultBranch != ""}
	return out, nil
}
