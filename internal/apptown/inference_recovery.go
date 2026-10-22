package apptown

import (
	"context"
	"log"
)

// RecoverInferenceRequests closes admissions interrupted by a daemon restart,
// and locks in the spend lines of AppTown's own calls still pending. It
// is part of RecoverInterruptedWork.
// Conversation identities and generated runtime homes are durable and untouched.
func (a *App) RecoverInferenceRequests(ctx context.Context) error {
	// A crash leaves admission receipts open. Do not invent a final token count.
	n, err := a.Usage.Requests.MarkInterrupted(ctx)
	if err != nil {
		return err
	}
	log.Printf("inference startup repair: marked %d unfinished receipts interrupted", n)
	return nil
}
