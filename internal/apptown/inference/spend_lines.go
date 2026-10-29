package inference

import (
	"context"
	"math"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/usage"
)

// spendLines is AppTown's own calls' accounting: a spend line opened before
// a call is sent and settled from its answer.
type spendLines struct{ requests *usage.Requests }

// open writes an AppTown call's pending spend line before anything is sent
// and returns the call's id. The line stays pending for the given time.
func (a spendLines) open(ctx context.Context, w identity.Workload, purpose, provider, model string, pending time.Duration) (ids.ID, error) {
	return a.requests.OpenCall(ctx, usage.Entry{Kind: purpose, ProjectID: w.ProjectID, RunID: w.RunID, Actor: w.Actor, Provider: provider, Model: model}, pending)
}

// settle settles an AppTown call's spend line.
func (a spendLines) settle(ctx context.Context, id ids.ID, u inferenceUsage, incomplete string) (bool, error) {
	return a.requests.SettleCall(ctx, id,
		usage.Totals{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Total: u.Input + u.Output, CostMicros: int64(math.Round(u.Cost * 1e6))}, incomplete)
}
