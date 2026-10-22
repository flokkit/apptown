package workflowexec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// Run deploy: the deploy step of a workflow, moving a standing deployment
// to the release the run selected.

// handleDeployments: GET /projects/{slug}/deployments
// RunDeploy executes a deploy step: run the app as the project deployment
// (stable name + port), replacing any previous release. This is an effect —
// place it after the acceptance gate in workflows.
func (a *Shipping) RunDeploy(task *workflow.Execution) {
	step := task.Step()
	stepID := step.ID
	run := task.Run()
	var cfg deployConfig
	decoder := json.NewDecoder(bytes.NewReader(step.Config))
	decoder.DisallowUnknownFields()
	configErr := decoder.Decode(&cfg)
	if cfg.Deployment == "" {
		cfg.Deployment = "main" // the standing env that tracks main (design/subsystems/deployment.md)
	}
	finish := func(status workflow.StepState, evidence any) {
		task.Settle(workflow.EncodeOutcome(status, evidence, nil))
	}
	if claim, err := task.Claim(); err != nil {
		return
	} else {
		*step = *claim
	}
	if configErr != nil {
		finish(workflow.StatusError, map[string]any{"reason": "invalid deploy configuration: " + configErr.Error()})
		return
	}
	// A cold deploy (image build/pull, container boot, health probe) can run
	// output-silent for tens of seconds — heartbeat so it doesn't look hung.
	stopHB := a.progress.Heartbeat(stepID, "deploying")
	defer stopHB()
	defer workflow.CaptureProgress(task.Context(), a.db, a.progress, stepID, a.clock)
	logf := func(format string, args ...any) { a.progress.Append(stepID, fmt.Sprintf(format, args...)) }
	logf("Selecting the committed source for deployment %s", cfg.Deployment)

	source, err := workflow.ReadSourceIn(task.Context(), a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	ws := source.Workspace(a.cfg.DataDir)
	// A standing deployment reflects MAIN, not the change's worktree: two
	// concurrent landings each deploying their own branch would leave the
	// env missing whichever change deployed first. The project checkout's
	// HEAD is main post-merge — deploy that.
	if source.RepoBacked() {
		ws = source.Repository(a.cfg.DataDir)
	}
	relSHA, err := (deploy.SelectedSource{Repository: ws, Revision: "HEAD"}).Resolve(task.Context())
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": "selecting deployment source: " + err.Error()})
		return
	}
	logf("Staging the selected commit %s", relSHA)
	tree, cleanup, err := deploy.StageCommit(task.Context(), ws, relSHA, "")
	selectedTree := tree.Directory
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": "staging deployment source: " + err.Error()})
		return
	}
	defer cleanup()
	selectedContract, err := docker.ReadSelectedContract(selectedTree)
	if err == docker.ErrNoContract {
		logf("Checking the committed source builds; no runtime deployment is declared")
		// CLI/library projects have nothing to deploy — but the merge IS
		// the release, so verify the COMMITTED tree compiles from a clean
		// checkout before recording it. webalizer-ng shipped with parity
		// receipts while main could not build: an unanchored .gitignore
		// pattern had silently excluded the whole implementation from every
		// commit, and no gate ever compiled what actually landed. Receipts
		// must be true of the merged tree, not the worktree.
		reason, ok, stopped := cleanTreeBuilds(task.Context(), selectedTree, a.checks,
			agentenv.AmbientFor(integration.RegistryBindingsIn(task.Context(), a.db), selectedTree))
		if stopped {
			return // the step stays running for its recovery
		}
		if !ok {
			task.Settle(workflow.EncodeRework(map[string]any{
				"reason": "merge-only release refused: the committed tree does not build from a clean checkout — " + reason}))
			return
		}
		recordArtifact(task.Context(), a.db, run.ID, "release", cfg.Deployment, "merge-only (no runtime contract)", "", a.now())

		finish(workflow.StatusPass, map[string]any{"skipped": "no .apptown/app.yaml — nothing to deploy; merge is the release",
			"cleanTreeBuild": "pass"})
		return
	}
	if err != nil {
		finish(workflow.StatusFail, map[string]any{"reason": err.Error()})
		return
	}

	slugMeta, slugErr := project.ByIDIn(task.Context(), a.db, run.ProjectID)
	slug := slugMeta.Slug
	if err := slugErr; err != nil {
		finish(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	logf("Resolving the target for deployment %s", cfg.Deployment)
	env, err := a.landingDeployment(task.Context(), run.ProjectID, cfg.Deployment)
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": "naming the deployment: " + err.Error()})
		return
	}
	names, err := env.HostNames(slug)
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": "naming the deployment: " + err.Error()})
		return
	}
	name := names.App()
	raw, _ := os.ReadFile(filepath.Join(selectedTree, ".apptown", "app.yaml"))
	// What the deployment would refuse at boot for this commit (a missing
	// setting, a contract its target cannot run) is refused here, before a
	// release is built or published; boot judges again (Preflight).
	logf("Checking %s's runtime settings for commit %s before building", cfg.Deployment, relSHA)
	check, err := a.deployment.Preflight(task.Context(), deployment.PreflightInput{ProjectID: run.ProjectID, DeploymentID: env.ID, Commit: relSHA, Workspace: selectedTree, Contract: selectedContract})
	var refused *deployment.PreflightError
	if errors.As(err, &refused) {
		logf("Preflight refused %s (%s): %s", cfg.Deployment, refused.Category, refused.Error())
		finish(workflow.StatusError, map[string]any{"reason": "release refused: " + refused.Error(), "preflight": refused.Evidence()})
		return
	} else if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": "checking the release before building: " + err.Error()})
		return
	}
	logf("Preflight passed for %s on commit %s at %s; boot checks again", check.Deployment, check.Commit, check.CheckedAt.UTC().Format(time.RFC3339))
	// The data it already holds is copied before anything is built or
	// booted, and read so it can be checked afterwards.
	dataClass := a.deploymentDataClass(task.Context(), run.ProjectID, cfg.Deployment)
	var witness ReleaseWitness
	if a.releaseData != nil {
		logf("Copying the data %s holds before releasing onto it", cfg.Deployment)
		w, err := a.releaseData.BeforeRelease(task.Context(), run.ProjectID, cfg.Deployment, dataClass == "synthetic")
		copied, words := w != nil && w.Complete(), ""
		switch {
		case err != nil:
			words = "the copy before this release could not be taken: " + err.Error()
		case w != nil:
			words = w.CopyWords()
			witness = w
		}
		logf("%s", words)
		if !copied {
			accepted := acceptanceIn(step.Config)
			if accepted != nil && accepted.Attempt != int64(step.Attempt) {
				accepted = nil // given for another attempt
			}
			if refused := releaseOverUncopied(cfg.Deployment, dataClass, accepted, words, logf); refused != "" {
				finish(workflow.StatusError, map[string]any{"reason": refused, "copy": words})
				return
			}
		}
	}
	// On an AppTown with a worker the release is built there, published and
	// committed as a digest bundle before anything is deployed; the host
	// pulls it.
	var bundle *deployment.ReleaseBundle
	if a.builds != nil {
		var ok bool
		if bundle, ok = a.buildRelease(task, releaseTarget{deployment: env, repository: ws, commit: relSHA, tree: selectedTree, contract: selectedContract, raw: raw}); !ok {
			return
		}
	}

	// Reserve a port only after the deployment has an explicit target.
	hostPort, err := a.deployment.Deployments.StablePort(task.Context(), run.ProjectID, cfg.Deployment, name)
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": "recording the deployment: " + err.Error()})
		return
	}
	env.HostPort, env.Container = hostPort, name
	art, err := docker.SourceArtifact(deploy.Source{Workspace: selectedTree, Revision: relSHA, Slug: slug, Contract: selectedContract}, cfg.Services)
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": "selecting the deployment revision: " + err.Error()})
		return
	}
	ws = art.Source.Workspace
	c := art.Source.Contract
	inputs := deployment.Inputs{ReleaseRun: run.ID, Revision: relSHA, ArtifactHash: art.Hash, DockerSource: string(art.Body)}
	if bundle != nil {
		inputs.BundleID = bundle.ID // the generation runs this bundle, whatever a later attempt commits
	}
	logf("Preparing the runtime deployment %s", cfg.Deployment)
	out, err := a.deployment.RunLanding(task.Context(), task, deployment.LandingInput{
		Log:    logf,
		Start:  deployment.Start{DeploymentID: env.ID, TargetID: env.TargetID, Key: fmt.Sprintf("run-%d-deploy-%d-attempt-%d", run.ID, stepID, step.Attempt), Inputs: inputs},
		Source: deploy.Source{Workspace: ws, Revision: relSHA, Slug: slug, Contract: c}, Services: cfg.Services, HostPort: hostPort, DataClass: dataClass,
		Bundle: bundle,
	})
	result := out.Execution
	if out.Boot.Err != nil {
		a.deployment.Deployments.ForgetTakenPort(task.Context(), run.ProjectID, cfg.Deployment, out.Boot.Err)
	}
	var failure *deployment.RunError
	if errors.As(err, &failure) && failure.Persistence != nil {
		log.Printf("deployment %d: %v", result.Attempt.ID, err)
	}
	if decision := landingDeploymentDecision(result, err, out.Boot); decision != nil {
		if decision.rework {
			task.Settle(workflow.EncodeRework(decision.evidence))
		} else {
			finish(decision.status, decision.evidence)
		}
		return
	}
	url := out.Boot.URL

	// The bundle (§2.8): what this landing built, cut once, for every
	// deployment of the project to run from now on. A bundle built on the
	// worker was committed before the deploy.
	if bundle == nil && cfg.Deployment == "main" && relSHA != "" {
		if _, err := a.cutBundle(task, run.ProjectID, relSHA, out, raw, deployment.BundleOrigin{ChangeID: run.ChangeID, RunID: run.ID}); err != nil {
			log.Printf("run %d: cutting the release bundle: %v", run.ID, err)
		}
	}
	// The deploy's artifact, its run event and its receipt are one fact.
	deployed := a.now()
	logf("Recording the deployed release at %s", url)
	recordArtifact(task.Context(), a.db, run.ID, "release", cfg.Deployment, url, "", deployed)
	depEv := out.Evidence
	if bundle != nil {
		depEv.Bundle = bundle.ID
	}
	recordEvent(task.Context(), a.db, run.ID, "release.deployed", map[string]any{"deployment": cfg.Deployment, "url": url, "digest": depEv.Digest}, deployed)
	a.backups.AfterRelease(run.ProjectID, cfg.Deployment)
	if witness != nil {
		problems, notes := witness.After(task.Context(), selectedContract.Needs)
		for _, n := range notes {
			logf("Not compared: %s", n)
		}
		if len(problems) > 0 {
			// The release is out; what is said is what is true.
			finish(workflow.StatusError, map[string]any{"reason": fmt.Sprintf("release booted and is running at %s, but its data continuity check failed: %s. %s; this check restored nothing and changed no data", url, strings.Join(problems, "; "), witness.CopyWords()), "continuity": problems, "copy": witness.CopyWords()})
			return
		}
		logf("The data its needs held before the release is still there")
	}
	audit.At(deployed).BestEffort(task.Context(), a.db, "apptown-system", run.ProjectID, "release.deployed", cfg.Deployment,
		map[string]any{"runId": run.ID, "url": url, "image": depEv.Image, "digest": depEv.Digest})

	logf("Deployment completed: %s", url)
	finish(workflow.StatusPass, depEv)
}

// releaseOverUncopied is why a landing may not release over data the copy
// before it did not hold ("" when it may): synthetic data may go, and a
// person may accept releasing anyway on retrying the step.
func releaseOverUncopied(deployment, dataClass string, accepted *workflow.UncopiedDataAcceptance, words string, logf func(string, ...any)) string {
	switch {
	case dataClass == "synthetic":
		logf("Releasing anyway: %s holds synthetic data", deployment)
		return ""
	case accepted != nil:
		logf("Releasing anyway: %s accepted releasing over data AppTown could not copy, at %s", accepted.By, accepted.At.Format(time.RFC3339))
		return ""
	}
	return "release refused: " + words + ". AppTown will not release over data it could not copy. Make that data copyable (a volume no service mounts may be data a service made again left behind: look at it first), or a person retries this step accepting uncopied data (apptown retry <change> --accept-uncopied-data)"
}

// acceptanceIn is the acceptance a retry put in a deploy step's
// configuration, under its exact key only: a JSON field would also take
// "AcceptUncopiedData" or any other casing, which only a workflow could
// have written.
func acceptanceIn(config []byte) *workflow.UncopiedDataAcceptance {
	var cfg map[string]json.RawMessage
	if json.Unmarshal(config, &cfg) != nil {
		return nil
	}
	raw, ok := cfg["acceptUncopiedData"]
	if !ok {
		return nil
	}
	var a workflow.UncopiedDataAcceptance
	if json.Unmarshal(raw, &a) != nil || a.By == "" {
		return nil
	}
	return &a
}

// deployConfig is a deploy step's options, and the acceptance a person's
// retry records.
type deployConfig struct {
	Deployment string               `json:"deployment"`
	Services   []deploy.ServiceSpec `json:"services"`
	// AcceptUncopiedData is read by its exact key (acceptanceIn): JSON
	// would take any casing of it.
	AcceptUncopiedData json.RawMessage `json:"acceptUncopiedData,omitempty"`
}
