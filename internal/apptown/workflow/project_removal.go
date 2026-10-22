package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

var ErrProjectBusy = errors.New("project has running work; abandon its runs before deleting it")

// RemoveProjectIn is Work's project-purge participant. The caller owns
// authorization, the transaction and its final project-deletion receipt.
// Project-first locking serializes admission; terminal Run locks prevent a
// concurrent writer from changing the history being removed. No external work
// happens here. An error requires the caller to roll back the transaction.
func RemoveProjectIn(ctx context.Context, tx *database.Tx, projectID ids.ID) error {
	if _, err := lockProjectRemovalIn(ctx, tx, projectID); err != nil {
		return err
	}
	q := workflowsql.New(tx.SQL())
	// Check each deletion before the next one so a suppressed write is attributed
	// to its own table, including children whose parent would later disappear.
	deletes := []struct {
		name    string
		delete  func(context.Context, ids.ID) error
		remains func(workflowsql.ProjectRemovalRemainderRow) bool
	}{
		{"agent_transcripts", q.DeleteProjectTranscripts, func(r workflowsql.ProjectRemovalRemainderRow) bool { return r.Transcripts }},
		{"steps", q.DeleteProjectSteps, func(r workflowsql.ProjectRemovalRemainderRow) bool { return r.Steps }},
		{"run_events", q.DeleteProjectRunEvents, func(r workflowsql.ProjectRemovalRemainderRow) bool { return r.Events }},
		{"artifacts", q.DeleteProjectArtifacts, func(r workflowsql.ProjectRemovalRemainderRow) bool { return r.Artifacts }},
		{"runs", q.DeleteProjectRuns, func(r workflowsql.ProjectRemovalRemainderRow) bool { return r.Runs }},
		{"changes", q.DeleteProjectChanges, func(r workflowsql.ProjectRemovalRemainderRow) bool { return r.Changes }},
	}
	for _, operation := range deletes {
		if err := operation.delete(ctx, projectID); err != nil {
			return err
		}
		remainder, err := q.ProjectRemovalRemainder(ctx, projectID)
		if err != nil {
			return err
		}
		if operation.remains(remainder) {
			return fmt.Errorf("project removal left Work %s rows", operation.name)
		}
	}
	return nil
}

func lockProjectRemovalIn(ctx context.Context, tx *database.Tx, projectID ids.ID) ([]ids.ID, error) {
	if projectID.IsZero() {
		return nil, fmt.Errorf("project removal requires positive scope")
	}
	q := workflowsql.New(tx.SQL())
	if _, err := q.ProjectForUpdate(ctx, projectID); err != nil {
		return nil, err
	}
	rows, err := q.ProjectRunsForRemoval(ctx, projectID)
	if err != nil {
		return nil, err
	}
	running := false
	idList := []ids.ID{}
	for _, row := range rows {
		state, err := ParseRunState(row.Status)
		if err != nil {
			return nil, err
		}
		running = running || state == RunRunning
		idList = append(idList, row.ID)
	}
	if running {
		return nil, ErrProjectBusy
	}
	return idList, nil
}

// RemovalCounts describes exactly the Work rows observed under the purge locks.
type RemovalCounts struct {
	Runs      int   `json:"runs"`
	Changes   int   `json:"changes"`
	Steps     int64 `json:"steps"`
	Events    int64 `json:"events"`
	Artifacts int64 `json:"artifacts"`
}

type ProjectRemoval struct {
	RunIDs, ChangeIDs []ids.ID
	PreviewStepIDs    []ids.ID // run-step previews' stacks are named by step
	// Transcripts are the digests of the project's kept agent transcripts;
	// the cleanup removes each file no remaining receipt references.
	Transcripts []string
	Counts      RemovalCounts
}

// ProjectRemovalIn prepares the external cleanup inventory and receipt counts
// inside the same transaction that will call RemoveProjectIn. It is an admitted
// administrative operation, not a public unscoped history reader.
func ProjectRemovalIn(ctx context.Context, tx *database.Tx, projectID ids.ID) (ProjectRemoval, error) {
	var out ProjectRemoval
	var err error
	out.RunIDs, err = lockProjectRemovalIn(ctx, tx, projectID)
	if err != nil {
		return out, err
	}
	q := workflowsql.New(tx.SQL())
	changes, err := q.ProjectChangesForRemovalMeta(ctx, projectID)
	if err != nil {
		return out, err
	}
	out.ChangeIDs = append([]ids.ID{}, changes...)
	previews, err := q.ProjectPreviewStepsForRemovalMeta(ctx, projectID)
	if err != nil {
		return out, err
	}
	out.PreviewStepIDs = append([]ids.ID{}, previews...)
	transcripts, err := q.ProjectTranscriptDigests(ctx, projectID)
	if err != nil {
		return out, err
	}
	out.Transcripts = append([]string{}, transcripts...)
	out.Counts.Runs, out.Counts.Changes = len(out.RunIDs), len(out.ChangeIDs)
	counts, err := q.ProjectRemovalCounts(ctx, projectID)
	out.Counts.Steps, out.Counts.Events, out.Counts.Artifacts = counts.Steps, counts.Events, counts.Artifacts
	return out, err
}
