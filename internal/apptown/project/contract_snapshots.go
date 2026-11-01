package project

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/strs"
)

// Contract snapshots preserve the native declaration as evidence. Launches
// read their selected Git tree; snapshots never modify or supply that source.

const maxSnapshotBytes = 256 * 1024 // an app.yaml far past any real contract

// snapshotContractAt records the committed native bytes, idempotently.
func (c *Contracts) RecordCommit(ctx context.Context, dataDir string, projectID ids.ID, sha string) {
	ctx = context.WithoutCancel(ctx) // the commit landed: its contract is kept though ctx has ended
	if projectID.IsZero() || sha == "" {
		return
	}
	contracts := c
	if done, err := contracts.snapshotted(ctx, projectID, sha); snapshotReadOK(err) && done {
		return // already recorded: idempotent
	}
	status, format, appYAML, errMsg := c.resolveSnapshotAt(ctx, dataDir, projectID, sha)
	if err := contracts.Snapshot(ctx, projectID, sha, status, format, appYAML, errMsg); err != nil {
		log.Printf("project %d: recording the contract as of %s: %v", projectID, strs.Truncate(sha, 10), err)
	}
}

// resolveSnapshotAt distinguishes an absent declaration from unreadable source.
func (c *Contracts) resolveSnapshotAt(ctx context.Context, dataDir string, projectID ids.ID, sha string) (status, format, appYAML, errMsg string) {
	slugMeta, slugErr := ByIDIn(ctx, c.db, projectID)
	if slugErr != nil {
		return "invalid", "none", "", "project not found"
	}
	repo, err := slugMeta.Repository(dataDir)
	if err != nil {
		return "invalid", "none", "", err.Error()
	}
	raw, err := docker.ContractFileAt(ctx, repo, sha)
	if errors.Is(err, docker.ErrNoContract) {
		return "absent", "none", "", ""
	}
	if err != nil {
		return "invalid", "none", "", err.Error()
	}
	if len(raw) > maxSnapshotBytes {
		return "invalid", "app-yaml", "", fmt.Sprintf("app.yaml is %d bytes (cap %d)", len(raw), maxSnapshotBytes)
	}
	if _, err := appcontract.Parse(raw); err != nil {
		return "invalid", "app-yaml", "", err.Error()
	}
	return "present", "app-yaml", string(raw), ""
}

func snapshotReadOK(err error) bool {
	found, err := database.Lookup(err)
	if err != nil {
		log.Printf("read failed: %v", err)
	}
	return found
}
