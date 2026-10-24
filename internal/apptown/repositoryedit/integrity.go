package repositoryedit

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/strs"
)

type Integrity struct {
	locks     *scm.RepositoryLocks
	runner    execution.Runner
	publisher Publisher
	db        *database.DB
	dataDir   string
	clock     func() time.Time // AppTown's clock; nil is the real one
	// git runs a discard's commands (gitexec.Run); a test stops its caller
	// between them.
	git func(ctx context.Context, dir string, args ...string) (string, error)
}

func (editor *Integrity) now() time.Time { return clock.Or(editor.clock)() }

func NewIntegrity(locks *scm.RepositoryLocks, runner execution.Runner, publisher Publisher, db *database.DB, dataDir string, now func() time.Time) *Integrity {
	return &Integrity{locks: locks, runner: runner, publisher: publisher, db: db, dataDir: dataDir, clock: now, git: gitexec.Run}
}

type IntegrityResult struct {
	OK     bool     `json:"ok"`
	Clean  bool     `json:"clean,omitempty"`
	Action string   `json:"action,omitempty"`
	Files  []string `json:"files,omitempty"`
}

// Resolve records the administrator's choice about edits outside sanctioned writers.
func (editor *Integrity) Resolve(ctx context.Context, p *project.Project, action, who string) (IntegrityResult, error) {
	if action != "adopt" && action != "discard" {
		return IntegrityResult{}, fmt.Errorf("unsupported integrity action %q", action)
	}
	repo := filepath.Join(editor.dataDir, "workspaces", "project-"+p.Slug)
	release, err := editor.locks.Acquire(ctx, p.ID)
	if err != nil {
		return IntegrityResult{}, fmt.Errorf("acquiring repository: %w", err)
	}
	defer release()
	drift, err := scm.Drift(ctx, editor.runner, repo)
	if err != nil {
		return IntegrityResult{}, fmt.Errorf("%w", err)
	}
	if len(drift) == 0 {
		return IntegrityResult{OK: true, Clean: true}, nil
	}
	// The diff is the evidence of what the decision keeps or destroys.
	diff, err := editor.git(ctx, repo, "diff")
	if err != nil {
		return IntegrityResult{}, fmt.Errorf("reading what the edits change: %w", err)
	}
	evidence := map[string]any{"files": drift, "diff": strs.Truncate(diff, 8192)}
	settle := context.WithoutCancel(ctx)
	switch action {
	case "adopt":
		if _, err := gitexec.Run(ctx, repo, "add", "-A"); err != nil {
			return IntegrityResult{}, fmt.Errorf("staging: %w", err)
		}
		msg := fmt.Sprintf("apptown: adopt out-of-band edits (decided by %s)\n\n"+
			"These files were changed outside AppTown; an admin chose to keep them.", who)
		if _, err := gitexec.Run(ctx, repo, "commit", "-m", msg); err != nil {
			return IntegrityResult{}, fmt.Errorf("committing: %w", err)
		}
		// The commit is the act, kept in history; its receipt is required.
		recordErr := scm.RecordMain(ctx, editor.db, repo, p.ID, p.DefaultBranch, scm.ViaIntegrityAdopt, editor.now())
		editor.publisher.PushBranch(ctx, p.ID, repo, p.DefaultBranch)
		if err := audit.At(editor.now()).Record(settle, editor.db, who, p.ID, "integrity.out-of-band-edits", action, evidence); err != nil {
			return IntegrityResult{}, fmt.Errorf("kept the edits, but their receipt was not recorded: %w", err)
		}
		if recordErr != nil {
			// The edits are kept; the older record stays, and the project's
			// main note shows main ahead of it.
			return IntegrityResult{}, fmt.Errorf("kept the edits, but recording where main stands failed: %w", recordErr)
		}
	case "discard":
		// A discard cannot be undone: it is requested on the record, with
		// what it destroys, before it starts, and its outcome recorded
		// after. Restoring the tracked files and removing the untracked ones
		// are one discard: once started it finishes though the caller stops.
		if err := audit.At(editor.now()).Record(ctx, editor.db, who, p.ID, "integrity.discard-requested", action, evidence); err != nil {
			return IntegrityResult{}, fmt.Errorf("not discarded: the request could not be recorded: %w", err)
		}
		failed := func(step string, err error) (IntegrityResult, error) {
			err = fmt.Errorf("%s: %w", step, err)
			if recErr := audit.At(editor.now()).Record(settle, editor.db, who, p.ID, "integrity.discard-failed", action, map[string]any{"files": drift, "error": err.Error()}); recErr != nil {
				return IntegrityResult{}, fmt.Errorf("%w; and recording that it failed: %v", err, recErr)
			}
			return IntegrityResult{}, err
		}
		if _, err := editor.git(settle, repo, "checkout", "--", "."); err != nil {
			return failed("restoring", err)
		}
		if out, err := editor.git(settle, repo, "clean", "-fd", "-e", ".apptown"); err != nil {
			return failed("removing the untracked files", fmt.Errorf("%v: %s", err, out))
		}
		if err := audit.At(editor.now()).Record(settle, editor.db, who, p.ID, "integrity.out-of-band-edits", action, evidence); err != nil {
			return IntegrityResult{}, fmt.Errorf("discarded the edits, but the outcome was not recorded: %w", err)
		}
	}
	return IntegrityResult{OK: true, Action: action, Files: drift}, nil
}
