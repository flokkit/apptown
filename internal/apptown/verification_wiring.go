package apptown

import (
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/worker"
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
	return verification.NewChanges(verification.ChangeDependencies{Now: a.cfg.clock.Now, DB: a.DB, Adoption: a.Adoption, Scanners: a.scanners, Evaluator: verification.NewEvaluator(a.evaluator, a.cfg.clock.Now), DataDir: a.cfg.dataDir, WorkerChats: a.chatSaves})
}
func guidanceFor(a *App) *verification.Guidance {
	return verification.NewGuidance(verification.GuidanceDependencies{DB: a.DB, DataDir: a.cfg.dataDir, Adoption: a.Adoption, Conformance: a.conformance, Scanners: a.scanners, Evaluator: verification.NewEvaluator(a.evaluator, a.cfg.clock.Now), Changes: a.verification, Runtime: runtimeFor(a), Docker: &a.docker, Previews: a.Preview, Routing: a.cfg.routing(), Selection: a.modelSelection, Ledger: a.Usage.Ledger, Now: a.cfg.clock.Now})
}

// checksFor is AppTown's check executor: the worker's when one is
// configured, and then nothing runs a check or prepares its image on this
// machine; otherwise this machine's Docker (design/operations/workers.md,
// "Checks on the worker").
func checksFor(a *App) verification.Executor {
	if at := a.cfg.worker; at != nil {
		w := worker.Checks{URL: at.URL, Client: at.Client, Limits: a.cfg.taskLimits}
		// The worker builds its agent image from the same context, named
		// by it (worker.AgentImage).
		image, _ := worker.AgentImage(a.cfg.agentContext)
		return verification.Executor{Run: w.Run, Image: w.Image, AgentImage: image}
	}
	return verification.LocalExecutor(projectProcesses(a), &a.docker, a.cfg.agentImage, a.cfg.clock.Now)
}

// taskPlacement is where checks, scripts and agents run: the worker, else
// this machine.
func taskPlacement(cfg configuration) workflow.Placement {
	if cfg.worker != nil {
		return workflow.Placement{Worker: cfg.worker.URL}
	}
	return workflow.Placement{Local: true}
}
