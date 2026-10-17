package forge

import (
	"context"
	"path/filepath"
	"time"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/worker"
)

func scannersFor(a *App) *verification.Scanners {
	return verification.NewScanners(verification.ScannerDependencies{Now: a.cfg.clock.Now, DB: a.DB, Docker: &a.docker, Executor: checksFor(a), Conformance: a.conformance, Evaluator: verification.NewEvaluator(a.evaluator, a.cfg.clock.Now), Integrations: a.Integrations, Deployments: a.Deployment.Deployments, Dependencies: a.Dependencies})
}
func scanStepsFor(a *App) *verification.ScanSteps {
	return verification.NewScanSteps(verification.ScanDependencies{DB: a.DB, Scanners: a.scanners, Conformance: a.conformance, Integrations: a.Integrations, Progress: &a.progress, Executor: checksFor(a), DataDir: a.cfg.dataDir, Panics: jobReporter{}, Now: a.cfg.clock.Now})
}

func runtimeFor(a *App) *verification.Runtime {
	return verification.NewRuntime(verification.RuntimeDependencies{Deployment: a.Deployment, Hosts: a.hosts, DB: a.DB, Docker: &a.docker, DataDir: a.cfg.dataDir, BrowserValues: a.Integrations.BrowserEnvironment})
}

func changesFor(a *App) *verification.Changes {
	return verification.NewChanges(verification.ChangeDependencies{Now: a.cfg.clock.Now, DB: a.DB, Adoption: a.Adoption, Conformance: a.conformance, Scanners: a.scanners, Evaluator: verification.NewEvaluator(a.evaluator, a.cfg.clock.Now), DataDir: a.cfg.dataDir, WorkerChats: a.chatSaves})
}
func guidanceFor(a *App) *verification.Guidance {
	return verification.NewGuidance(verification.GuidanceDependencies{DB: a.DB, DataDir: a.cfg.dataDir, Adoption: a.Adoption, Conformance: a.conformance, Scanners: a.scanners, Evaluator: verification.NewEvaluator(a.evaluator, a.cfg.clock.Now), Changes: a.verification, Runtime: runtimeFor(a), Docker: &a.docker, Previews: a.Preview, Routing: a.cfg.routing(), Selection: a.modelSelection, Ledger: a.Usage.Ledger, Now: a.cfg.clock.Now})
}

// checksFor is the forge's check executor: the check worker's when one is
// configured, else the worker's, and then nothing runs a check or prepares
// its image on this machine; otherwise this machine's Docker
// (design/operations/workers.md, "Checks on the worker").
func checksFor(a *App) verification.Executor {
	if at := checkWorkerOf(a.cfg); at != nil {
		w := worker.Checks{URL: at.URL, Client: at.Client, Limits: a.cfg.taskLimits}
		// The worker builds its agent image from the same context, named
		// by it (worker.AgentImage).
		image, _ := worker.AgentImage(a.cfg.agentContext)
		return verification.Executor{Run: w.Run, Image: w.Image, AgentImage: image}
	}
	return verification.LocalExecutor(projectProcesses(a), &a.docker, a.cfg.agentImage, a.cfg.clock.Now)
}

// checkWorkerOf is the worker checks run on: the check worker, else the
// worker, else none (this machine).
func checkWorkerOf(cfg configuration) *execution.WorkerTasks {
	if cfg.checkWorker != nil {
		return cfg.checkWorker
	}
	return cfg.worker
}

// checkPlacement is where the check executor runs, recorded on every check
// result; with a check worker, gates accept only results recorded with it.
// Only the check worker's role is recorded: the one it reported at start.
func checkPlacement(cfg configuration) workflow.Placement {
	if cfg.checkWorker != nil {
		return workflow.Placement{Worker: cfg.checkWorker.URL, Role: worker.RoleCheck}
	}
	return taskPlacement(cfg)
}

// taskPlacement is where scripts and agents run: the worker, else this machine.
func taskPlacement(cfg configuration) workflow.Placement {
	if cfg.worker != nil {
		return workflow.Placement{Worker: cfg.worker.URL}
	}
	return workflow.Placement{Local: true}
}

// requireCheckWorker records this forge's first use of each worker address,
// and refuses to start with a check worker that does not report the check
// role, fixed before the forge first used its address.
func requireCheckWorker(ctx context.Context, cfg configuration) error {
	if cfg.verifyOnly {
		return nil
	}
	dir := filepath.Join(cfg.dataDir, "workers-used")
	if cfg.worker != nil {
		if _, err := worker.FirstUse(dir, cfg.worker.URL, cfg.clock.Now); err != nil {
			return err
		}
	}
	if cfg.checkWorker == nil {
		return nil
	}
	first, err := worker.FirstUse(dir, cfg.checkWorker.URL, cfg.clock.Now)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return worker.RequireCheckWorker(ctx, cfg.checkWorker.Client, cfg.checkWorker.URL, first)
}
