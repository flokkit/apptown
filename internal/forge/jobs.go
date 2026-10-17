package forge

import (
	"context"
	"log"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/project"
)

// Jobs: the forge's own work (design/subsystems/jobs.md). River owns queue
// execution and retries; these handlers own domain outcomes and external-effect
// identity. A queue receipt is diagnostic, never authority to repeat a release.

// backupAfterRelease takes a set once a release is live on a Docker host,
// in the background: a release is the moment the data is worth keeping
// and the moment before the next change to it.
func (b backupRequests) AfterRelease(projectID ids.ID, deploymentName string) {
	// It follows a release that has happened, from callers with no context
	// to give: asked on the App's lifetime, though that ends.
	ctx := context.WithoutCancel(b.life)
	p, _ := project.ByIDIn(ctx, b.db, projectID)
	if err := b.RequestBackup(ctx, p.Slug+"/"+deploymentName, "release"); err != nil {
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
