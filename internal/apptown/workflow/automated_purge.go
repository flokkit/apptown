package workflow

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
)

type workRetention interface {
	RetainsWorkIn(context.Context, database.Queryer, ids.ID, []ids.ID) (bool, error)
}

// PurgeAutomatedChanges retries disposal of abandoned, untouched automated
// work. A durable claim makes it unavailable to new human acts before external
// cleanup. Files and Git never run inside a database transaction. Retained
// summaries and scan observations have independent lifetimes.
func (s *Service) PurgeAutomatedChanges(ctx context.Context) error {
	rows, err := workflowsql.New(s.db.SQL()).AutomatedPurgeCandidates(ctx)
	if err != nil {
		return err
	}
	if len(rows) > 0 && s.retention == nil {
		return fmt.Errorf("automated work retention is unavailable")
	}
	var failed []error
	for _, row := range rows {
		if err := s.purgeAutomatedChange(ctx, row.ProjectID, row.ID); err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}

func (s *Service) purgeAutomatedChange(ctx context.Context, projectID, changeID ids.ID) error {
	closure, err := scm.PrepareClosure(ctx, s.abandonments.locks, projectID)
	if err != nil {
		return err
	}
	defer closure.Close()
	var repo, branch string
	var runIDs []ids.ID
	var digests []string
	claimed := false
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		q := workflowsql.New(tx.SQL())
		p, err := q.ProjectForUpdate(ctx, projectID)
		if err != nil {
			return err
		}
		row, err := q.ChangeForUpdate(ctx, workflowsql.ChangeForUpdateParams{ID: changeID, ProjectID: projectID})
		if err != nil {
			return err
		}
		if !row.Automated || row.State != ChangeAbandoned.String() {
			return nil
		}
		busy, err := q.AutomatedPurgeBusy(ctx, changeID)
		if err != nil || busy {
			return err
		}
		runs, err := q.ChangeRuns(ctx, workflowsql.ChangeRunsParams{ProjectID: projectID, ChangeID: changeID})
		if err != nil {
			return err
		}
		for _, r := range runs {
			runIDs = append(runIDs, r.ID)
		}
		retained, err := s.retention.RetainsWorkIn(ctx, tx, changeID, runIDs)
		if err != nil || retained {
			return err
		}
		ws := (Layout{DataDir: s.dataDir}).ChangeWorkspace(changeID)
		if s.workspaceChats != nil && s.workspaceChats.UsesWorkspace(ctx, ws) {
			return nil
		}
		if err := closure.CheckIn(ctx, tx, projectID, runIDs); err != nil {
			return err
		}
		n, err := q.ClaimAutomatedPurge(ctx, changeID)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrRunMoved
		}
		digests, err = q.ChangeTranscriptDigests(ctx, changeID)
		if err != nil {
			return err
		}
		repo, err = (project.RepositoryRef{ID: p.ID, Slug: p.Slug}).Path(s.dataDir)
		if err != nil {
			return err
		}
		branch = row.Branch
		claimed = true
		return nil
	})
	if err != nil || !claimed {
		return err
	}
	// A claimed Change is logically gone. Repeating cleanup after a crash is safe.
	for _, id := range append([]ids.ID{changeID}, runIDs...) {
		paths := []string{(Layout{DataDir: s.dataDir}).RunWorkspace(id), filepath.Join(s.dataDir, "transcripts", fmt.Sprintf("run-%d", id))}
		if id == changeID {
			paths = []string{(Layout{DataDir: s.dataDir}).ChangeWorkspace(id), filepath.Join(s.dataDir, "transcripts", fmt.Sprintf("change-%d", id))}
		}
		for _, path := range paths {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		}
	}
	if branch != "" {
		if tip, err := gitexec.HubReadRef(ctx, repo, "refs/heads/"+branch); err != nil {
			return err
		} else if tip != "" {
			if _, err := gitexec.Run(ctx, repo, "update-ref", "-d", "refs/heads/"+branch, tip); err != nil {
				return err
			}
		}
	}
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		q := workflowsql.New(tx.SQL())
		if _, err := q.ProjectForUpdate(ctx, projectID); err != nil {
			return err
		}
		if err := q.PurgeAutomatedRuns(ctx, changeID); err != nil {
			return err
		}
		n, err := q.PurgeAutomatedChange(ctx, changeID)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrRunMoved
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := RemoveUnreferencedTranscripts(ctx, s.db, s.dataDir, digests); err != nil {
		log.Printf("automated Change %s: transcript cleanup will be retried by the reaper: %v", changeID, err)
		return err
	}
	return nil
}
