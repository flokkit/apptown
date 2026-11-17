package deployment

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// JanitorHold is derived from settled releases and uncertain open execution.
// Queued requests and a recovery still running cannot clear a failed release.
type JanitorHold struct {
	DeploymentID  ids.ID `json:"deploymentId"`
	Deployment    string `json:"deployment"`
	DisplayLabel  string `json:"displayLabel"`
	ReleaseNumber int64  `json:"releaseNumber"`
	Reason        string `json:"reason"`
}

func (s *Service) HeldBack(ctx context.Context, project ids.ID) ([]JanitorHold, error) {
	return s.HeldBackIn(ctx, s.db, project)
}

// HeldBackIn lets admission read the same ledger under the project lock.
func (s *Service) HeldBackIn(ctx context.Context, q database.Queryer, project ids.ID) ([]JanitorHold, error) {
	rows, err := deploymentsql.New(q.SQL()).JanitorReleaseConclusion(ctx, project)
	if err != nil {
		return nil, err
	}
	out := []JanitorHold{}
	for _, r := range rows {
		if r.Reason != "" {
			out = append(out, JanitorHold{r.ID, r.Name, r.DisplayLabel, r.Number, r.Reason})
		}
	}
	return out, nil
}

func (h JanitorHold) String() string {
	return fmt.Sprintf("release %d to %s: %s", h.ReleaseNumber, h.DisplayLabel, h.Reason)
}
