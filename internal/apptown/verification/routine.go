package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/gitexec"
)

// routineInspector uses committed objects and the existing check executor; it
// never prepares a workspace or changes source to make it assessable.
type routineInspector struct {
	db       *database.DB
	fixes    func(context.Context, workflow.Source) ([]fix.Declaration, []check.Finding, error)
	dataDir  string
	locks    *scm.RepositoryLocks
	executor Executor
}

func NewRoutineInspector(dataDir string, locks *scm.RepositoryLocks, executor Executor) routineInspector {
	return routineInspector{dataDir: dataDir, locks: locks, executor: executor}
}

type routineTree struct {
	base, tip, tree string
	files           []scm.ObjectChange
}

func (r routineInspector) observe(ctx context.Context, source workflow.Source) (out routineTree, err error) {
	repo, err := source.Repository(r.dataDir)
	if err != nil {
		return out, err
	}
	workspace, err := source.Workspace(r.dataDir)
	if err != nil {
		return out, err
	}
	if r.locks == nil {
		return out, errors.New("repository observation is unavailable")
	}
	release, err := r.locks.Acquire(ctx, source.ProjectID)
	if err != nil {
		return out, err
	}
	// Only owned repository reads occur under this lock. Folder observations are
	// separate: writers can already hold the folder while publishing to the hub.
	err = func() error {
		defer release()
		read := func(args ...string) (string, error) {
			b, e := gitexec.HubOutput(ctx, repo, nil, args...)
			return strings.TrimSpace(string(b)), e
		}
		out.tip, err = read("rev-parse", "--verify", "--end-of-options", "refs/heads/"+source.Branch+"^{commit}")
		if err != nil {
			return err
		}
		out.base, err = scm.ForkBase(ctx, repo, repo, source.DefaultBranch, out.tip, source.ChangeBaseSHA)
		if err != nil {
			return err
		}
		out.tree, err = read("rev-parse", "--verify", "--end-of-options", out.tip+"^{tree}")
		if err != nil {
			return err
		}
		out.files, err = scm.ReadObjectChanges(ctx, repo, out.base, out.tip)
		if err != nil {
			return err
		}
		return nil
	}()
	if err != nil {
		return out, err
	}
	if _, err = os.Lstat(workspace); err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	folderTip, dirty, err := gitexec.ObserveFolder(ctx, workspace)
	if err != nil {
		return out, err
	}
	if dirty || folderTip != out.tip {
		return out, errors.New("source is dirty, unpublished or differs from the published candidate")
	}
	return out, nil
}

func (r routineInspector) InspectRoutine(ctx context.Context, source workflow.Source, categories workflow.StandingCategories) (workflow.StandingInspection, error) {
	return r.inspectRoutine(ctx, source, categories)
}

// InspectRoutineWithAdoption is used only after Adoption verified a nonempty,
// assessed target. An empty Git diff alone remains insufficient.
func (r routineInspector) InspectRoutineWithAdoption(ctx context.Context, source workflow.Source, categories workflow.StandingCategories) (workflow.StandingInspection, error) {
	out := workflow.StandingInspection{Classifier: "assessed-adoption-v1", Classification: "cannot-assess"}
	before, err := r.observe(ctx, source)
	if err != nil {
		out.Reason = err.Error()
		return out, nil
	}
	out.Base, out.Candidate, out.Tree = before.base, before.tip, before.tree
	for _, file := range before.files {
		out.ChangedFiles = append(out.ChangedFiles, file.Path)
	}
	out.Classification = "routine-adoption"
	return out, nil
}

func (r routineInspector) inspectRoutine(ctx context.Context, source workflow.Source, categories workflow.StandingCategories) (out workflow.StandingInspection, err error) {
	if categories.Dependencies == "" && !categories.SecurityFixes {
		return workflow.StandingInspection{Classification: "requires-person", Reason: "the adoption target needs a separate assessment"}, nil
	}
	return r.inspectUpdates(ctx, source, categories)
}

func observationDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
