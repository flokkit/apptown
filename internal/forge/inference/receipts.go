package inference

import (
	"context"
	"log"
	"math"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/usage"
	"flokkit.io/forge/internal/strs"
)

type receipts struct{ requests *usage.Requests }

func (a receipts) begin(ctx context.Context, w identity.Workload, provider, model string) (ids.ID, error) {
	return a.requests.Begin(ctx, usage.Request{Kind: w.Kind, Actor: w.Actor, Who: w.Who(), ProjectID: w.ProjectID, RunID: w.RunID, StepID: w.StepID, Attempt: w.Attempt, SessionID: w.SessionID, Provider: provider, Model: model})
}
func (a receipts) finish(ctx context.Context, id ids.ID, status int, outcome string, u inferenceUsage) {
	counted, err := strs.JSON(u)
	if err != nil {
		// The receipt still closes, saying its usage was not observed.
		log.Printf("inference receipt %d: %v", id, err)
		counted = `{"observed":false}`
	}
	if err := a.requests.Finish(ctx, id, status, outcome, counted); err != nil {
		log.Printf("inference receipt %d could not be finalized", id)
	}
}

// open writes an AppTown call's receipt and its pending spend line, before
// anything is sent. The line stays pending for the given time.
func (a receipts) open(ctx context.Context, w identity.Workload, purpose, provider, model string, pending time.Duration) (ids.ID, error) {
	return a.requests.OpenCall(ctx, usage.Request{Kind: w.Kind, Actor: w.Actor, ProjectID: w.ProjectID, RunID: w.RunID, Provider: provider, Model: model},
		usage.Entry{Kind: purpose, ProjectID: w.ProjectID, RunID: w.RunID, Actor: w.Actor, Provider: provider, Model: model}, pending)
}

// settle closes an AppTown call's receipt and settles its spend line.
func (a receipts) settle(ctx context.Context, id ids.ID, status int, outcome string, u inferenceUsage, incomplete string) (bool, error) {
	counted, err := strs.JSON(u)
	if err != nil {
		// The receipt still closes, saying its usage was not observed; the
		// spend line keeps the counts.
		log.Printf("inference receipt %d: %v", id, err)
		counted = `{"observed":false}`
	}
	return a.requests.SettleCall(ctx, id, status, outcome, counted,
		usage.Totals{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Total: u.Input + u.Output, CostMicros: int64(math.Round(u.Cost * 1e6))}, incomplete)
}
