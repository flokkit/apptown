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
	"github.com/flokkit/apptown/internal/oci"
	"github.com/flokkit/apptown/internal/routinechange"
)

// routineInspector uses committed objects and the existing check executor; it
// never prepares a workspace or changes source to make it assessable.
type routineInspector struct {
	dataDir      string
	locks        *scm.RepositoryLocks
	executor     Executor
	imageReader  func(context.Context, oci.Source) (oci.Observation, error)
	advisoryRead func(context.Context, string, string, []byte) ([]byte, error)
}

func NewRoutineInspector(dataDir string, locks *scm.RepositoryLocks, executor Executor) routineInspector {
	return routineInspector{dataDir: dataDir, locks: locks, executor: executor, imageReader: oci.Observe, advisoryRead: readRoutineAdvisory}
}

type routineTree struct {
	base, tip, tree                                          string
	unsupported                                              string
	files                                                    []scm.ObjectChange
	before, after                                            routinechange.NPMSnapshot
	dockerBefore, dockerAfter, contractBefore, contractAfter []byte
	npm, image                                               bool
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
		for _, file := range out.files {
			if file.Path == "Dockerfile" {
				out.image = true
			}
			if file.Path == "package.json" || file.Path == "package-lock.json" {
				out.npm = true
			}
		}
		blob := func(commit, name string) ([]byte, error) {
			raw, e := gitexec.HubOutput(ctx, repo, nil, "ls-tree", "-z", commit, "--", name)
			if e != nil {
				return nil, e
			}
			entries := strings.Split(string(raw), "\x00")
			if len(entries) != 2 || entries[1] != "" {
				return nil, fmt.Errorf("%s must be a committed regular file", name)
			}
			meta, path, ok := strings.Cut(entries[0], "\t")
			fields := strings.Fields(meta)
			if !ok || path != name || len(fields) != 3 || fields[0] != "100644" || fields[1] != "blob" {
				return nil, fmt.Errorf("%s has unsupported committed mode or type", name)
			}
			return gitexec.HubOutput(ctx, repo, nil, "cat-file", "blob", fields[2])
		}
		for i, commit := range []string{out.base, out.tip} {
			if out.npm {
				competing, e := gitexec.HubOutput(ctx, repo, nil, "ls-tree", "-z", commit, "--", "npm-shrinkwrap.json", ".npmrc", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb")
				if e != nil {
					return e
				}
				if len(competing) != 0 {
					out.unsupported = "the committed source contains npm configuration or a competing lock format requiring a person"
				}
				manifest, e := blob(commit, "package.json")
				if e != nil {
					return e
				}
				lock, e := blob(commit, "package-lock.json")
				if e != nil {
					return e
				}
				snapshot := routinechange.NPMSnapshot{Manifest: manifest, Lock: lock}
				if i == 0 {
					out.before = snapshot
				} else {
					out.after = snapshot
				}
			}
			if out.image {
				dockerfile, e := blob(commit, "Dockerfile")
				if e != nil {
					return e
				}
				contract, e := blob(commit, ".apptown/app.yaml")
				if e != nil {
					return e
				}
				if i == 0 {
					out.dockerBefore, out.contractBefore = dockerfile, contract
				} else {
					out.dockerAfter, out.contractAfter = dockerfile, contract
				}
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

func (r routineInspector) InspectRoutine(ctx context.Context, source workflow.Source, categories workflow.StandingCategories) (workflow.StandingInspection, error) {
	return r.inspectRoutine(ctx, source, categories, false)
}

// InspectRoutineWithAdoption is used only after Adoption verified a nonempty,
// completely classified target. An empty Git diff alone remains insufficient.
func (r routineInspector) InspectRoutineWithAdoption(ctx context.Context, source workflow.Source, categories workflow.StandingCategories) (workflow.StandingInspection, error) {
	return r.inspectRoutine(ctx, source, categories, true)
}

func (r routineInspector) inspectRoutine(ctx context.Context, source workflow.Source, categories workflow.StandingCategories, adoption bool) (out workflow.StandingInspection, err error) {
	out.Classifier = routinechange.Version + "+node-digest-v1+osv-npm-v1"
	out.Classification = "cannot-assess"
	before, err := r.observe(ctx, source)
	out.Base, out.Candidate, out.Tree = before.base, before.tip, before.tree
	for _, file := range before.files {
		out.ChangedFiles = append(out.ChangedFiles, file.Path)
	}
	if err != nil {
		out.Reason = err.Error()
		return out, nil
	}
	refuse := func(reason string) (workflow.StandingInspection, error) {
		out.Classification = "requires-person"
		out.Reason = reason
		return out, nil
	}
	if before.unsupported != "" {
		return refuse(before.unsupported)
	}
	if len(before.files) == 0 && !adoption {
		return refuse("the candidate has no supported update")
	}
	for _, file := range before.files {
		allowed := file.Path == "package.json" || file.Path == "package-lock.json" || file.Path == "Dockerfile"
		if !allowed || file.Status != "M" || file.BeforeMode != "100644" || file.AfterMode != "100644" {
			return refuse("the complete diff includes files or modes outside supported updates")
		}
	}
	var npm routinechange.NPMCandidate
	var image routinechange.ImageCandidate
	if before.npm {
		npm, err = routinechange.CompareNPM(before.before, before.after, categories.Dependencies)
		if err != nil {
			return refuse(err.Error())
		}
	}
	if before.image {
		if !categories.BaseImages {
			return refuse("base-image updates are not permitted by this rule")
		}
		image, err = routinechange.CompareImage(before.dockerBefore, before.dockerAfter)
		if err != nil {
			return refuse(err.Error())
		}
		if err = eligibleImageContract(before.contractBefore, before.contractAfter); err != nil {
			return refuse(err.Error())
		}
	}
	// Explain the entire diff before any external observation or execution.
	if before.npm {
		evidence, e := validateNPM(ctx, r.executor, r.dataDir, before.before, before.after, npm)
		if e != nil {
			out.Reason = e.Error()
			return out, nil
		}
		out.Validation = &workflow.StandingValidation{Image: evidence.Image, Node: evidence.Node, NPM: evidence.NPM, Platform: evidence.Platform, ReportDigest: evidence.ReportDigest, ScratchSettlement: evidence.ScratchSettlement}
	}
	if before.image {
		evidence, e := r.validateImage(ctx, image)
		if e != nil {
			out.Reason = e.Error()
			return out, nil
		}
		out.Images = evidence
	}
	if categories.SecurityFixes {
		advisory := workflow.StandingAdvisoryObservation{Status: "unsupported", Reason: "image OS advisory mapping is not supported"}
		if before.npm {
			advisory = r.observeAdvisories(ctx, npm)
		}
		if before.image && before.npm {
			advisory.Reason += "; image OS advisory mapping is not supported"
		}
		out.Advisories = &advisory
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
	out.Classification = "routine-dependency"
	if len(before.files) == 0 {
		out.Classification = "routine-adoption"
	}
	if before.image {
		out.Classification = "routine-image"
		if before.npm {
			out.Classification = "routine-dependency-and-image"
		}
	}
	out.Reason = "the exact update is within the selected bounds; classification does not accept, land or deploy it"
	return out, nil
}
