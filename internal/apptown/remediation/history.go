package remediation

import (
	"context"
	"slices"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/remediation/internal/remediationsql"
	"github.com/flokkit/apptown/internal/database"
)

// minimumHistory is the fewest settled attempts that say anything about a
// check's cost: fewer is "no history", never an estimate.
const minimumHistory = 3

// AttemptHistoryIn is what fixing one block's check has cost before, at any
// version, from settled attempts only: their count, median cost and
// duration, and the share that landed. nil when there are fewer than three.
func AttemptHistoryIn(ctx context.Context, q database.Queryer, block, check string) (*conformance.AttemptHistory, error) {
	rows, err := queries(q).SettledAttemptsForCheck(ctx, remediationsql.SettledAttemptsForCheckParams{Block: block, CheckName: check})
	if err != nil || len(rows) < minimumHistory {
		return nil, err
	}
	costs, minutes, landed := make([]float64, 0, len(rows)), make([]float64, 0, len(rows)), 0
	for _, r := range rows {
		costs = append(costs, float64(r.CostMicros)/1e6)
		minutes = append(minutes, r.FinishedAt.Sub(r.StartedAt).Minutes())
		if r.Outcome == "landed" {
			landed++
		}
	}
	return &conformance.AttemptHistory{Attempts: len(rows), MedianCost: median(costs), MedianMinutes: median(minutes),
		SucceededShare: float64(landed) / float64(len(rows))}, nil
}

func median(xs []float64) float64 {
	slices.Sort(xs)
	n := len(xs)
	if n%2 == 1 {
		return xs[n/2]
	}
	return (xs[n/2-1] + xs[n/2]) / 2
}
