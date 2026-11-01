package apptown

import (
	"context"
	"log"
)

// recoverInferenceRequests locks in the spend lines of AppTown's own calls
// still pending when the daemon restarted. It is part of
// RecoverInterruptedWork.
// Conversation identities and generated runtime homes are durable and untouched.
func (a *App) recoverInferenceRequests(ctx context.Context) error {
	// A crash cut those calls off. Do not invent a final token count.
	n, err := a.Usage.Requests.MarkInterrupted(ctx)
	if err != nil {
		return err
	}
	log.Printf("inference startup repair: locked in %d unsettled calls as unknown spend", n)
	return nil
}
