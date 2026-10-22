package apptown

import (
	"context"
	"log"
)

// Startup queues observation of interrupted deployments before dispatch and
// never contacts providers; the job observes one attempt through Deployment.
func (a *App) RecoverDeployments(ctx context.Context) error {
	queued, err := a.Deployment.PrepareRecovery(ctx)
	if err != nil {
		return err
	}
	log.Printf("deployment recovery: observation_queued=%d", queued)
	return nil
}
