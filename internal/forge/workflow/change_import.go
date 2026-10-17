package workflow

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/gitexec"
)

// Git is what Work asks of the repositories on disk: a standalone clone
// for a change or a run, the human attachments copied into it, and two
// questions about branches an import needs answered. GitWorkspace is the
// implementation over real git; a test supplies its own.
type Git interface {
	EnsureClone(ctx context.Context, in CloneRequest) (WorkspaceResult, error)
	// SyncAttachments copies a change's attachments into a workspace (the
	// change's folder, or a run's clone) under .forge/attachments.
	SyncAttachments(ctx context.Context, changeID ids.ID, workspace string) error
	// BringToBranch fast-forwards a change's folder to its published branch.
	BringToBranch(ctx context.Context, workspace, repository, branch string) error
	BranchTip(ctx context.Context, repository, branch string) (string, error)
	MergeBase(ctx context.Context, repository, a, b string) (string, error)
	Paths() Layout
}

// Layout is where a project's repository and a workspace live.
type Layout struct{ DataDir string }

func (l Layout) Repository(slug string) string {
	return filepath.Join(l.DataDir, "workspaces", "project-"+slug)
}
func (l Layout) ChangeWorkspace(changeID ids.ID) string {
	return filepath.Join(l.DataDir, "workspaces", fmt.Sprintf("change-%d", changeID))
}

// GitWorkspace receives execution and repository location as values. Its commands
// are bounded and cancellation reaches the process group via execution.Runner.
type GitWorkspace struct {
	DataDir string
	Runner  ProcessRunner
}

var _ Git = GitWorkspace{}

func (g GitWorkspace) Paths() Layout { return Layout{DataDir: g.DataDir} }
func (g GitWorkspace) BranchTip(ctx context.Context, repository, branch string) (string, error) {
	// Fully qualified refs prevent revision expressions/options from masquerading as branches.
	return g.git(ctx, repository, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
}
func (g GitWorkspace) MergeBase(ctx context.Context, repository, a, b string) (string, error) {
	return g.git(ctx, repository, "merge-base", "refs/heads/"+a, b)
}

// ProcessRunner substitutes execution at the edge; it has no database or Work authority.
type ProcessRunner interface {
	Run(context.Context, string, execution.Request) execution.Outcome
}

func (g GitWorkspace) command(ctx context.Context, dir string, args ...string) execution.Outcome {
	runner := g.Runner
	if runner == nil {
		runner = execution.Runner{}
	}
	return runner.Run(ctx, execution.Local, execution.Request{Workspace: dir, Argv: append([]string{"git"}, args...), Timeout: 10 * time.Minute})
}
func (g GitWorkspace) git(ctx context.Context, dir string, args ...string) (string, error) {
	out := g.command(ctx, dir, args...)
	if err := gitOutcomeError(ctx, out); err != nil {
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out.Output)), nil
}
func gitOutcomeError(ctx context.Context, out execution.Outcome) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if out.RunErr != nil {
		return fmt.Errorf("%w: %s", out.RunErr, strings.TrimSpace(string(out.Output)))
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("git exited %d: %s", out.ExitCode, strings.TrimSpace(string(out.Output)))
	}
	return nil
}

type ImportBranch struct {
	ProjectID     ids.ID
	Branch, Title string
}
type importSource struct {
	projectID                              ids.ID
	slug, defaultBranch, branch, base, tip string
}
type changeImporter struct {
	creator Creator
	git     Git
	locks   *scm.RepositoryLocks
}

func newImporter(db *database.DB, git Git, locks *scm.RepositoryLocks, prepare func(context.Context, CreatedChange) error) changeImporter {
	return changeImporter{NewCreator(db, prepare), git, locks}
}
func (i changeImporter) Import(ctx context.Context, in ImportBranch, who string) (*Change, error) {
	if in.ProjectID.IsZero() || strings.TrimSpace(who) == "" {
		return nil, fmt.Errorf("invalid branch import")
	}
	release, err := i.locks.Acquire(ctx, in.ProjectID)
	if err != nil {
		return nil, err
	}
	created, err := func() (CreatedChange, error) {
		defer release()
		source := importSource{projectID: in.ProjectID, branch: in.Branch}
		project, err := workflowsql.New(i.creator.records.db.SQL()).ProjectByID(ctx, in.ProjectID)
		if err != nil {
			return CreatedChange{}, err
		}
		source.slug, source.defaultBranch = project.Slug, project.DefaultBranch
		if source.defaultBranch == "" {
			return CreatedChange{}, fmt.Errorf("project %q is not repo-backed", source.slug)
		}
		if err := gitexec.CheckBranchName(in.Branch); err != nil {
			return CreatedChange{}, fmt.Errorf("invalid branch: %w", err)
		}
		repo := i.git.Paths().Repository(source.slug)
		source.tip, err = i.git.BranchTip(ctx, repo, in.Branch)
		if err != nil {
			return CreatedChange{}, fmt.Errorf("branch %q not found in the project repo: %w", in.Branch, err)
		}
		source.base, err = i.git.MergeBase(ctx, repo, source.defaultBranch, source.tip)
		if err != nil {
			return CreatedChange{}, err
		}
		if source.tip == source.base {
			return CreatedChange{}, fmt.Errorf("branch %q has no commits beyond %s — nothing to import", in.Branch, source.defaultBranch)
		}
		return i.creator.records.importBranch(ctx, in, source, who)
	}()
	if err != nil {
		return nil, err
	}
	return i.creator.prepareCreated(ctx, created)
}
func (s changeStore) importBranch(ctx context.Context, in ImportBranch, source importSource, who string) (CreatedChange, error) {
	now := s.now()
	return database.Transact(ctx, s.db, func(tx *database.Tx) (CreatedChange, error) {
		project, err := workflowsql.New(tx.SQL()).ProjectForUpdate(ctx, in.ProjectID)
		if err != nil {
			return CreatedChange{}, err
		}
		if err := refuseUnlessActive(ctx, tx, in.ProjectID); err != nil {
			return CreatedChange{}, err
		}
		current := CreatedChange{Slug: project.Slug, DefaultBranch: project.DefaultBranch}
		if source.projectID != in.ProjectID || source.branch != in.Branch || current.Slug != source.slug || current.DefaultBranch != source.defaultBranch || source.base == "" || source.tip == "" {
			return CreatedChange{}, fmt.Errorf("project or import source changed; inspect and import again")
		}
		title := in.Title
		if title == "" {
			title = "import " + in.Branch
		}
		created, err := insertChangeIn(ctx, tx, ChangeDraft{ProjectID: in.ProjectID, Title: title}, who, current, changeOrigin{branch: source.branch, base: source.base, provenance: "imported/branch"}, now)
		if err != nil {
			return CreatedChange{}, err
		}
		receipt := struct {
			Branch   string `json:"branch"`
			Base     string `json:"base"`
			Evidence string `json:"evidence"`
		}{source.branch, source.base, "commits only (reconstructed)"}
		if err := audit.At(now).RecordIn(ctx, tx, who, in.ProjectID, "change.imported", fmt.Sprintf("change %d", created.Change.ID), receipt); err != nil {
			return CreatedChange{}, err
		}
		return created, nil
	})
}
