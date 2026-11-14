package workcommands

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/fault"
)

type candidateWorkflows interface {
	CandidateWorkflow(context.Context, adoption.ReceiptWorkflow) (*adoption.ResolvedWorkflow, error)
}

// adoptionProcedure selects the publication procedure from the prepared pins.
func adoptionProcedure(ctx context.Context, ways candidateWorkflows, c adoption.Candidate) (*adoption.ResolvedWorkflow, string, error) {
	for _, pin := range c.Receipt.Workflows {
		w, err := ways.CandidateWorkflow(ctx, pin)
		if err != nil {
			return nil, "", err
		}
		if trigger, ok := w.Doc.Standing(); ok && trigger.Publication {
			return w, trigger.Queue, nil
		}
	}
	return nil, "", fault.New(fault.Unprocessable, "this adoption brings no adoption procedure; include the platform pack")
}
