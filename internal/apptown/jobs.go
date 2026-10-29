package apptown

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"log"
	"strconv"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/database"
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
