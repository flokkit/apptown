package workflow

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"
)

type dashboard struct {
	WaitingOnYou []RunSummary
	ActiveRuns   []RunSummary
}

// Dashboard partitions readable current work once on the server. Classification
// selects the holding decision before reader eligibility decides membership.
// Each change appears once. Personal work is unlimited; other visible work
// retains the newest 20 rows.
func (s *Service) Dashboard(ctx context.Context) (dashboard, error) {
	rows, err := s.Runs(ctx, RunQuery{State: RunRunning, RootsOnly: true})
	if err != nil {
		return dashboard{}, err
	}
	return partitionHomeWork(rows), nil
}

func partitionHomeWork(rows []RunSummary) dashboard {
	out := dashboard{WaitingOnYou: []RunSummary{}, ActiveRuns: []RunSummary{}}
	seen := map[ids.ID]bool{}
	for _, row := range rows {
		if !row.ChangeID.IsZero() {
			if seen[row.ChangeID] {
				continue
			}
			seen[row.ChangeID] = true
		}
		if row.WaitingOn.You == "yes" {
			out.WaitingOnYou = append(out.WaitingOnYou, row)
		} else if len(out.ActiveRuns) < 20 {
			out.ActiveRuns = append(out.ActiveRuns, row)
		}
	}
	return out
}
