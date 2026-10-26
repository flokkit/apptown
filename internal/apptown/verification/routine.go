package verification

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/routinechange"
)

// RoutineInspector uses committed objects and the existing check executor; it
// never prepares a workspace or changes source to make it assessable.
type RoutineInspector struct {
	dataDir  string
	locks    *scm.RepositoryLocks
	executor Executor
}

func NewRoutineInspector(dataDir string, locks *scm.RepositoryLocks, executor Executor) RoutineInspector {
	return RoutineInspector{dataDir, locks, executor}
}

type routineTree struct {
	base, tip, tree string
	unsupported     string
	files           []scm.ObjectChange
	before, after   routinechange.NPMSnapshot
}

func (r RoutineInspector) observe(ctx context.Context, source workflow.Source) (out routineTree, err error) {
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
		out.base, err = scm.ForkBase(ctx, repo, source.DefaultBranch, out.tip, source.ChangeBaseSHA)
		if err != nil {
			return err
		}
		out.tree, err = read("rev-parse", "--verify", "--end-of-options", out.tip+"^{tree}")
		if err != nil {
			return err
		}
		for _, commit := range []string{out.base, out.tip} {
			competing, e := gitexec.HubOutput(ctx, repo, nil, "ls-tree", "-z", commit, "--", "npm-shrinkwrap.json", ".npmrc", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb")
			if e != nil {
				return e
			}
			if len(competing) != 0 {
				out.unsupported = "the committed source contains npm configuration or a competing lock format requiring a person"
			}

			raw, e := gitexec.HubOutput(ctx, repo, nil, "ls-tree", "-z", commit, "--", "package.json", "package-lock.json")
			if e != nil {
				return e
			}
			entries := strings.Split(string(raw), "\x00")
			if len(entries) != 3 || entries[2] != "" {
				return errors.New("both npm inputs must be present as committed regular files")
			}
			for _, entry := range entries[:2] {
				meta, name, ok := strings.Cut(entry, "\t")
				fields := strings.Fields(meta)
				if !ok || len(fields) != 3 || fields[0] != "100644" || fields[1] != "blob" || (name != "package.json" && name != "package-lock.json") {
					return errors.New("npm input has an unsupported committed mode or type")
				}
			}
		}
		out.files, err = scm.ReadObjectChanges(ctx, repo, out.base, out.tip)
		if err != nil {
			return err
		}
		for _, file := range out.files {
			if (file.Path != "package.json" && file.Path != "package-lock.json") || file.Status != "M" || file.BeforeMode != "100644" || file.AfterMode != "100644" {
				continue
			}
			before, e := gitexec.HubOutput(ctx, repo, nil, "cat-file", "blob", file.BeforeObject)
			if e != nil {
				return e
			}
			after, e := gitexec.HubOutput(ctx, repo, nil, "cat-file", "blob", file.AfterObject)
			if e != nil {
				return e
			}
			if file.Path == "package.json" {
				out.before.Manifest, out.after.Manifest = before, after
			} else {
				out.before.Lock, out.after.Lock = before, after
			}
		}
		// A lock-only update still needs both unchanged manifest blobs. Read by the
		// exact commit; never silently replace missing content with an empty object.
		if len(out.before.Manifest) == 0 {
			out.before.Manifest, err = gitexec.HubOutput(ctx, repo, nil, "show", out.base+":package.json")
			if err != nil {
				return err
			}
			out.after.Manifest, err = gitexec.HubOutput(ctx, repo, nil, "show", out.tip+":package.json")
			if err != nil {
				return err
			}
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

func (r RoutineInspector) InspectRoutine(ctx context.Context, source workflow.Source, bounds string) (out workflow.StandingInspection, err error) {
	out.Classifier = routinechange.Version
	out.Classification = "cannot-assess"
	before, err := r.observe(ctx, source)
	if err != nil {
		out.Reason = err.Error()
		return out, nil
	}
	out.Base, out.Candidate, out.Tree = before.base, before.tip, before.tree
	for _, file := range before.files {
		out.ChangedFiles = append(out.ChangedFiles, file.Path)
	}
	if before.unsupported != "" {
		out.Classification = "requires-person"
		out.Reason = before.unsupported
		return out, nil
	}
	for _, file := range before.files {
		if (file.Path != "package.json" && file.Path != "package-lock.json") || file.Status != "M" || file.BeforeMode != "100644" || file.AfterMode != "100644" {
			out.Classification = "requires-person"
			out.Reason = "the complete diff includes files or modes outside a bounded npm update"
			return out, nil
		}
	}
	candidate, err := routinechange.CompareNPM(before.before, before.after, bounds)
	if err != nil {
		out.Classification = "requires-person"
		out.Reason = err.Error()
		return out, nil
	}
	evidence, err := validateNPM(ctx, r.executor, r.dataDir, before.before, before.after, candidate)
	if err != nil {
		out.Reason = err.Error()
		return out, nil
	}
	after, err := r.observe(ctx, source)
	if err != nil {
		out.Reason = fmt.Sprintf("source cannot be confirmed after validation: %v", err)
		return out, nil
	}
	if !reflect.DeepEqual(before, after) {
		out.Reason = "source moved while dependency validation ran"
		return out, nil
	}
	out.Validation = &workflow.StandingValidation{Image: evidence.Image, Node: evidence.Node, NPM: evidence.NPM, Platform: evidence.Platform, ReportDigest: evidence.ReportDigest, ScratchSettlement: evidence.ScratchSettlement}
	out.Classification = "routine-dependency"
	out.Reason = "the exact dependency update is within the selected bounds; classification does not accept or land it"
	return out, nil
}
