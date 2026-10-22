package apptown

import (
	"context"
	"log"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// ReapPreviewOrphans removes every preview at startup, on this machine and on
// the worker: previews cannot outlive the apptownd that tracks them (deployed
// deployments DO survive).
// Nothing boots yet, so every boot's own Docker configuration left by a
// stop goes too.
func (a *App) ReapPreviewOrphans(ctx context.Context) {
	if !a.docker.Absent() { // no local Docker: nothing local to reap
		docker.ReapPreviewOrphans(ctx, a.docker)
		agentenv.ReapWorkshops(ctx, a.docker)
	}
	a.Preview.RemoveWorkerPreviews(ctx)
	if err := docker.SweepBootConfigs(a.hosts.BootScratch()); err != nil {
		log.Printf("removing boots' Docker configurations left by a stop: %v", err)
	}
}
