package apptown

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// Jobs: AppTown's own work (design/subsystems/jobs.md). River owns queue
// execution and retries; these handlers own domain outcomes and external-effect
// identity. A queue receipt is diagnostic, never authority to repeat a release.

// backupAfterRelease takes a set once a release is live on a Docker host,
// in the background: a release is the moment the data is worth keeping
// and the moment before the next change to it.
func (b backupRequests) AfterRelease(projectID ids.ID, deploymentName string) {
	// It follows a release that has happened, from callers with no context
	// to give: asked on the App's lifetime, though that ends.
	ctx := context.WithoutCancel(b.life)
	if err := b.RequestBackup(ctx, projectID.String()+"/"+deploymentName, "release"); err != nil {
		log.Printf("backup after release %d/%s: asking: %v", projectID, deploymentName, err)
	}
}

// Backup requests can be admitted before workers are constructed. Their durable
// format and one-attempt budget are identical to the scheduled backup job.
type backupRequests struct {
	db   *database.DB
	life context.Context // the App's lifetime, for AfterRelease
}

func (b backupRequests) RequestBackup(ctx context.Context, key, trigger string) error {
	_, err := database.Transact(ctx, b.db, func(tx *database.Tx) (bool, error) {
		err := jobQueue{}.EnqueueIn(ctx, tx, jobs.Request{Operation: "backup", Key: key, Values: map[string]any{"trigger": trigger}})
		return err == nil, err
	})
	return err
}

// All disk observation requests share one durable identity. River coalesces
// pending and running requests; handlers never collect disks on the read path.
type diskObservationRequests struct{ db *database.DB }

func (r diskObservationRequests) RequestDiskObservation(ctx context.Context) error {
	return r.db.InTx(ctx, func(tx *database.Tx) error {
		return jobQueue{}.EnqueueIn(ctx, tx, jobs.Request{Operation: diskObservationJob, Key: "instance"})
	})
}

// Inventory observation shares the existing provider refresh operation and key.
type inventoryRequests struct{ db *database.DB }

func (r inventoryRequests) RequestInventory(ctx context.Context, name string) error {
	return r.db.InTx(ctx, func(tx *database.Tx) error {
		return jobQueue{}.EnqueueIn(ctx, tx, jobs.Request{Operation: "inventory", Key: name})
	})
}

type capacityObservationRequests struct{ db *database.DB }

func (r capacityObservationRequests) RequestCapacityObservation(ctx context.Context, id ids.ID, revision int64) error {
	return r.db.InTx(ctx, func(tx *database.Tx) error {
		return jobQueue{}.EnqueueIn(ctx, tx, jobs.Request{Operation: deployment.CapacityObservationJob, Key: id.String() + "/" + strconv.FormatInt(revision, 10)})
	})
}

type protectionObservationRequests struct{ db *database.DB }

func (r protectionObservationRequests) RequestProtectionObservation(ctx context.Context, id ids.ID, identity string) error {
	return r.db.InTx(ctx, func(tx *database.Tx) error {
		return jobQueue{}.EnqueueIn(ctx, tx, jobs.Request{Operation: dataprotection.ProtectionObservationJob, Key: id.String() + "/" + identity})
	})
}

// A configured worker's observations have one durable identity, including while
// running. A new endpoint never inherits an old endpoint's queued observation.
type workerObservationRequests struct{ db *database.DB }

func (r workerObservationRequests) RequestWorkerObservation(ctx context.Context, identity string) error {
	return r.db.InTx(ctx, func(tx *database.Tx) error {
		return jobQueue{}.EnqueueIn(ctx, tx, jobs.Request{Operation: workerObservationJob, Key: identity})
	})
}

// infrastructureChecks binds periodic and post-act admission to the owners it
// needs. Domain callbacks retain these collaborators, not the whole App.
type infrastructureChecks struct {
	targets     *deployment.Targets
	deployments *deployment.Deployments
	protection  *dataprotection.ProtectionObserver
	inventory   *inventoryCache
	db          *database.DB
	life        context.Context
}

func infrastructureChecksFor(a *App) infrastructureChecks {
	return infrastructureChecks{targets: a.targets, deployments: a.deployments, protection: a.protectionObserver, inventory: a.inventory, db: a.DB, life: a.life}
}

// Run admits the same per-target jobs on every five-minute tick.
// The pass enumerates current targets, including the built-in local target.
func (c infrastructureChecks) Run(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
	var errs []error
	targets, err := c.targets.All(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("list targets: %w", err))
	}
	for _, target := range targets {
		if target.Status == deployment.TargetRetired {
			continue
		}
		if err := (inventoryRequests{db: c.db}).RequestInventory(ctx, target.Name); err != nil {
			errs = append(errs, fmt.Errorf("%s inventory: %w", target.Name, err))
		}
		if err := (capacityObservationRequests{db: c.db}).RequestCapacityObservation(ctx, target.ID, target.SettingsRevision); err != nil {
			errs = append(errs, fmt.Errorf("%s capacity: %w", target.Name, err))
		}
	}
	deployments, err := c.deployments.All(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("list deployments: %w", err))
	}
	for _, dep := range deployments {
		if dep.Kind != deployment.Standing {
			continue
		}
		if err := c.protection.Request(ctx, dep.ID); err != nil {
			errs = append(errs, fmt.Errorf("%s protection: %w", dep.ID, err))
		}
	}
	if err := (diskObservationRequests{db: c.db}).RequestDiskObservation(ctx); err != nil {
		errs = append(errs, fmt.Errorf("instance disks: %w", err))
	}
	return nil, errors.Join(errs...)
}

// Applied preserves old evidence and queues a post-act check even
// when a scheduled check is already running. Post-act checks coalesce per target;
// an act during one waits for the next periodic check to clear invalidation.
func (c infrastructureChecks) Applied(targetID ids.ID) {
	ctx := context.WithoutCancel(c.life)
	target, err := c.targets.ByID(ctx, targetID)
	if err != nil {
		log.Printf("infrastructure: finding changed target: %v", err)
		return
	}
	c.inventory.Invalidate(target.Name)
	err = c.db.InTx(ctx, func(tx *database.Tx) error {
		return jobQueue{}.EnqueueIn(ctx, tx, jobs.Request{Operation: "inventory", Key: target.Name + "/act", Values: map[string]any{"target": target.Name}})
	})
	if err != nil {
		log.Printf("infrastructure: scheduling %s after an act: %v", target.Name, err)
	}
}

// Request starts collection without waiting for the first periodic tick.
func (c infrastructureChecks) Request(ctx context.Context) error {
	return c.db.InTx(ctx, func(tx *database.Tx) error {
		return jobQueue{}.EnqueueIn(ctx, tx, jobs.Request{Operation: infrastructureCheckJob, Key: passScheduledKey})
	})
}
