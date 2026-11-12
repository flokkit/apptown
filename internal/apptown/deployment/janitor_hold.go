package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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
	deps, err := deploymentsql.New(q.SQL()).MainDeployments(ctx, project)
	if err != nil {
		return nil, err
	}
	out := []JanitorHold{}
	for _, dep := range deps {
		var setting RoutineUpdates
		if err := json.Unmarshal(dep.RoutineUpdates, &setting); err != nil {
			return nil, err
		}
		if setting.Mode == "person" {
			continue
		}
		rows, err := deploymentsql.New(q.SQL()).JanitorReleaseLedger(ctx, dep.ID)
		if err != nil {
			return nil, err
		}
		var latest *deploymentsql.JanitorReleaseLedgerRow
		var newest time.Time
		var newestID ids.ID
		uncertain := false
		for _, row := range rows {
			if row.State == Releasing.String() {
				progress, err := deploymentsql.New(q.SQL()).ReleaseRecoveryProgress(ctx, row.ID)
				if err != nil {
					return nil, err
				}
				if progress.Uncertain {
					out = append(out, JanitorHold{dep.ID, dep.Name, dep.DisplayLabel, row.Number, "release progress is uncertain"})
					uncertain = true
					break
				}
			}
			if row.State != Released.String() && row.State != ReleaseFailed.String() {
				continue
			}
			if latest == nil {
				copy := row
				latest = &copy
			}
			if row.BundleCreatedAt.Valid && (row.BundleCreatedAt.Time.After(newest) || row.BundleCreatedAt.Time.Equal(newest) && row.BundleID.Compare(newestID) > 0) {
				newest, newestID = row.BundleCreatedAt.Time, row.BundleID
			}
		}
		if uncertain || latest == nil {
			continue
		}
		reason := ""
		if latest.State == ReleaseFailed.String() {
			reason = "release failed"
		} else if latest.BundleCreatedAt.Valid && (latest.BundleCreatedAt.Time.Before(newest) || latest.BundleCreatedAt.Time.Equal(newest) && latest.BundleID.Compare(newestID) < 0) {
			reason = "runs earlier code"
		}
		if reason != "" {
			out = append(out, JanitorHold{dep.ID, dep.Name, dep.DisplayLabel, latest.Number, reason})
		}
	}
	return out, nil
}

func (h JanitorHold) String() string {
	return fmt.Sprintf("release %d to %s: %s", h.ReleaseNumber, h.DisplayLabel, h.Reason)
}
