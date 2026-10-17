package projectlifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/operations"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow"
)

// runPurge is the purge: every step makes something absent (or archived),
// and a step already done counts as done, so a later attempt after a
// failure or an unknown end repeats nothing harmful. The records go last,
// in one transaction with the deletion record that releases the slug once
// the cleanup of previews and workspaces is done.
func (d *Deletion) runPurge(ctx context.Context, op operations.Operation, log *operations.StepLog) operations.Outcome {
	slug, _ := op.Input["slug"].(string)
	fail := func(left []string, why string) operations.Outcome {
		log.Fail(ctx, why)
		detail := "purge incomplete: " + why
		if len(left) > 0 {
			detail += "; left: " + strings.Join(left, "; ")
		}
		return operations.Outcome{State: operations.Failed, Detail: detail, Result: map[string]any{"left": left}}
	}
	// err says "authority withdrawn" and why.
	refused := func(err error) operations.Outcome { return fail(nil, err.Error()) }

	log.Begin(ctx, "checking the project is deleted and idle")
	l, err := project.LifecycleIn(ctx, d.lifecycle.db, op.ProjectID)
	if err != nil {
		return fail(nil, err.Error())
	}
	if l.State != project.Deleted {
		return fail(nil, fmt.Sprintf("the project is %s, not deleted", l.State))
	}
	if err := d.lifecycle.db.InTx(ctx, func(tx *database.Tx) error {
		_, err := workflow.ProjectRemovalIn(ctx, tx, op.ProjectID)
		return err
	}); err != nil {
		return fail(nil, err.Error())
	}

	if err := log.StillAllowed(ctx); err != nil {
		return refused(err)
	}
	log.Begin(ctx, "removing its backup sets, on disk and in the store")
	removed, left, err := d.deps.Data.PurgeProjectBackups(ctx, op.ProjectID, log.StillAllowed)
	if errors.Is(err, operations.ErrAuthorityWithdrawn) {
		return refused(err)
	}
	if err != nil {
		return fail(left, "removing its backup sets: "+err.Error())
	}
	if len(left) > 0 {
		return fail(left, fmt.Sprintf("%d backup sets were removed and %d were not", removed, len(left)))
	}
	log.End(ctx, fmt.Sprintf("%d sets removed", removed), 0)

	if err := log.StillAllowed(ctx); err != nil {
		return refused(err)
	}
	log.Begin(ctx, "removing its hub repository")
	if err := os.RemoveAll(hubDir(d.lifecycle.dataDir, slug)); err != nil {
		return fail([]string{"the hub repository of " + slug}, err.Error())
	}
	log.End(ctx, "", 0)

	if path, _ := op.Input["gitlabPath"].(string); path != "" {
		if err := log.StillAllowed(ctx); err != nil {
			return refused(err)
		}
		log.Begin(ctx, "archiving its GitLab copy at "+path)
		id, _ := op.Input["gitlabProjectId"].(float64)
		archive, _ := op.Input["gitlabArchive"].(string)
		if err := d.deps.Archive.archiveGitLab(ctx, CleanupPlan{GitLabPath: path, GitLabProjectID: int64(id), GitLabArchive: archive}); err != nil {
			return fail([]string{"the GitLab copy at " + path}, err.Error())
		}
		log.End(ctx, "renamed to "+archive+" and archived", 0)
	}

	if err := log.StillAllowed(ctx); err != nil {
		return refused(err)
	}
	log.Begin(ctx, "removing its records")
	who := op.AskedBy
	if l.RequestedBy != "" {
		who = l.RequestedBy
	}
	plan := CleanupPlan{Slug: slug}
	removal, err := d.lifecycle.removeRecords(ctx, op.ProjectID, who, func(tx *database.Tx, removal workflow.ProjectRemoval) error {
		plan.RunIDs, plan.ChangeIDs, plan.PreviewSteps, plan.Transcripts = removal.RunIDs, removal.ChangeIDs, removal.PreviewStepIDs, removal.Transcripts
		at := d.now()
		// The deletion's requester is recorded with their principal at the
		// request; the operation's asker is resolved now.
		requester := access.Actor{Name: who, Principal: l.RequestedByPrincipal}
		if who != l.RequestedBy || l.RequestedByPrincipal.IsZero() {
			resolved, err := identity.ActorIn(ctx, tx, who)
			if err != nil {
				return err
			}
			requester = resolved
		}
		if err := project.RecordDeletionIn(ctx, tx, op.ProjectID, slug, requester, plan, at); err != nil {
			return err
		}
		if err := d.deps.Queue.EnqueueCleanupIn(ctx, tx, op.ProjectID, 0); err != nil {
			return err
		}
		return audit.At(at).RecordIn(ctx, tx, op.AskedBy, op.ProjectID, "project.purged", slug, map[string]any{
			"operation": op.ID, "binding": l.BindingID, "deleter": l.RequestedBy, "deletedAt": l.DeletedAt, "backupSets": removed,
			"credential": op.Authority.Credential})
	})
	if err != nil {
		return fail([]string{"the project's records"}, err.Error())
	}
	d.release(op.ProjectID)
	return operations.Outcome{State: operations.Succeeded, Detail: fmt.Sprintf("purged: %d backup sets, its hub repository and its records removed (%d runs); its previews and workspaces are being cleaned up", removed, removal.Counts.Runs),
		Result: map[string]any{"backupSets": removed, "runs": removal.Counts.Runs}}
}

// Cleanups lists recent purged projects' cleanups. A pending one whose job
// the queue has given up on is reported as needing attention, with why.
func (d *Deletion) Cleanups(ctx context.Context) ([]project.Deletion, error) {
	list, err := d.lifecycle.projects.Deletions(ctx)
	if err != nil {
		return nil, err
	}
	err = d.lifecycle.db.InTx(ctx, func(tx *database.Tx) error {
		for i := range list {
			if list[i].State != project.CleanupPending {
				continue
			}
			live, err := d.deps.Queue.LiveIn(ctx, tx, list[i].ProjectID, list[i].Round)
			if err != nil {
				return err
			}
			if !live {
				list[i].State = project.CleanupNeedsAttention
				list[i].Detail = strings.TrimSpace("the queue stopped trying this cleanup before it finished. " + list[i].Detail)
			}
		}
		return nil
	})
	return list, err
}

// RetryCleanup starts a new round of a cleanup that needs attention: the
// round, its job and the receipt naming who asked commit together. A cleanup
// still pending whose job is no longer live also needs attention: the queue
// can give up on a job (a failed settlement still spends a try) before the
// record counts its last attempt.
func (d *Deletion) RetryCleanup(ctx context.Context, projectID ids.ID, who string) error {
	return d.lifecycle.db.InTx(ctx, func(tx *database.Tx) error {
		current, err := project.DeletionForUpdateIn(ctx, tx, projectID)
		if err != nil {
			return err
		}
		switch current.State {
		case project.CleanupDone:
			return project.ErrCleanupNotRetryable
		case project.CleanupPending:
			if live, err := d.deps.Queue.LiveIn(ctx, tx, projectID, current.Round); err != nil {
				return err
			} else if live {
				return ErrCleanupStillRunning
			}
		}
		round, err := project.RestartDeletionIn(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if err := d.deps.Queue.EnqueueCleanupIn(ctx, tx, projectID, round); err != nil {
			return err
		}
		return audit.At(d.now()).RecordIn(ctx, tx, who, ids.Nil, "project.cleanup.retried", fmt.Sprint(projectID), map[string]any{"round": round})
	})
}
