package workflowexec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// RunBuild builds the committed tree without preparing any runtime or data.
func (a *shipping) RunBuild(task *workflow.Execution) {
	run := task.Run()
	step, err := task.Claim()
	if err != nil {
		return
	}
	finish := func(status workflow.StepState, evidence any) {
		task.Settle(workflow.EncodeOutcome(status, evidence, nil))
	}
	fail := func(err error) { finish(workflow.StatusError, map[string]any{"reason": err.Error()}) }
	stop := a.progress.Heartbeat(step.ID, "building")
	defer stop()
	defer workflow.CaptureProgress(task.Context(), a.db, a.progress, step.ID, a.clock)
	source, err := workflow.ReadSourceIn(task.Context(), a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		fail(err)
		return
	}
	ws, err := source.Workspace(a.cfg.DataDir)
	if source.RepoBacked() {
		ws, err = source.Repository(a.cfg.DataDir)
	}
	if err != nil {
		fail(err)
		return
	}
	revision, err := (deploy.SelectedSource{Repository: ws, Revision: "HEAD"}).Resolve(task.Context())
	if err != nil {
		fail(err)
		return
	}
	tree, cleanup, err := deploy.StageCommit(task.Context(), ws, revision, "")
	if err != nil {
		fail(err)
		return
	}
	defer cleanup()
	contract, err := docker.ReadSelectedContract(tree.Directory)
	if errors.Is(err, docker.ErrNoContract) {
		bound, err := integration.RegistryBindings(task.Context(), a.db)
		if err != nil {
			fail(err)
			return
		}
		reason, ok, stopped := cleanTreeBuilds(task.Context(), tree.Directory, a.checks, agentenv.AmbientFor(bound, tree.Directory))
		if stopped {
			return
		}
		if !ok {
			finish(workflow.StatusFail, map[string]any{"reason": reason})
			return
		}
		if err := task.InAttempt("build", func(tx *database.Tx, _ int64) error {
			return recordArtifact(task.Context(), tx, run.ID, "build", "no-runtime", revision, "clean-tree build; no runtime declared", a.now())
		}); err != nil {
			fail(err)
			return
		}
		finish(workflow.StatusPass, map[string]any{"cleanTreeBuild": "pass", "runtime": false, "commit": revision})
		return
	}
	if err != nil {
		fail(err)
		return
	}
	raw, err := os.ReadFile(filepath.Join(tree.Directory, ".apptown", "app.yaml"))
	if err != nil {
		fail(err)
		return
	}
	var bundle *deployment.ReleaseBundle
	if a.builds != nil {
		var ok bool
		bundle, ok = a.buildRelease(task, releaseTarget{repository: ws, commit: revision, tree: tree.Directory, contract: contract, raw: raw}, finish)
		if !ok {
			return
		}
	} else {
		bundle, err = a.deployment.BuildRelease(task.Context(), deployment.SourceBuild{By: task, ProjectID: run.ProjectID, ChangeID: run.ChangeID, RunID: run.ID, Tree: tree.Directory, Revision: revision, Contract: contract, Raw: raw, Log: func(format string, args ...any) { a.progress.Append(step.ID, fmt.Sprintf(format, args...)) }})
		if err != nil {
			fail(err)
			return
		}
	}
	if bundle == nil {
		fail(errors.New("runtime declared but the build produced no bundle"))
		return
	}
	if err := task.InAttempt("build", func(tx *database.Tx, _ int64) error {
		return recordArtifact(task.Context(), tx, run.ID, "build", "runtime-bundle", bundle.ID.String(), "", a.now())
	}); err != nil {
		fail(err)
		return
	}
	finish(workflow.StatusPass, map[string]any{"bundle": bundle.ID, "commit": revision, "runtime": true})
}
