package apptown

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/worker"
)

func scannersFor(a *App) *verification.Scanners {
	return verification.NewScanners(verification.ScannerDependencies{Now: a.cfg.clock.Now, DB: a.DB, Docker: &a.docker, Executor: checksFor(a), Conformance: a.conformance, Evaluator: checkEvaluatorFor(a), Integrations: a.Integrations, Deployments: a.Deployment.Deployments, Dependencies: a.Dependencies})
}
func scanStepsFor(a *App) *verification.ScanSteps {
	return verification.NewScanSteps(verification.ScanDependencies{DB: a.DB, Scanners: a.scanners, Conformance: a.conformance, Integrations: a.Integrations, Progress: &a.progress, Executor: checksFor(a), DataDir: a.cfg.dataDir, Panics: jobReporter{}, Now: a.cfg.clock.Now})
}

func runtimeFor(a *App) *verification.Runtime {
	return verification.NewRuntime(verification.RuntimeDependencies{Environment: a.environment, Deployment: a.Deployment, Hosts: a.hosts, DB: a.DB, Docker: &a.docker, DataDir: a.cfg.dataDir, BrowserValues: a.Integrations.BrowserEnvironment})
}

func changesFor(a *App) *verification.Changes {
	return verification.NewChanges(verification.ChangeDependencies{Now: a.cfg.clock.Now, DB: a.DB, Adoption: a.Adoption, Scanners: a.scanners, Evaluator: checkEvaluatorFor(a), DataDir: a.cfg.dataDir, WorkerChats: a.chatSaves})
}
func guidanceFor(a *App) *verification.Guidance {
	return verification.NewGuidance(verification.GuidanceDependencies{DB: a.DB, DataDir: a.cfg.dataDir, Adoption: a.Adoption, Conformance: a.conformance, Scanners: a.scanners, Evaluator: checkEvaluatorFor(a), Changes: a.verification, Runtime: runtimeFor(a), Docker: &a.docker, Previews: a.Preview, Routing: a.cfg.routing(), Selection: a.modelSelection, Ledger: a.Usage.Ledger, Now: a.cfg.clock.Now})
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

// checkEvaluatorFor selects one backend for check sessions. There is no local
// retry when the configured worker is unavailable.
func checkJudgeFor(a *App) *verification.CheckJudge {
	j := &verification.CheckJudge{Now: a.cfg.clock.Now, Driver: a.tasks.Driver, Remote: a.cfg.worker != nil, Models: checkModelConfiguration{runtime: inferenceRuntimeFor(a), enabled: a.cfg.inferenceURL != ""}, Sessions: &a.checkSessions, Capacity: a.capacity, Directory: filepath.Join(a.cfg.dataDir, "check-sessions"), Profile: a.tasks.Profile.Name, Limits: a.tasks.Profile.Limits, TimeLimit: defaultCheckTimeCap, TokenLimit: defaultCheckTokenCap}
	j.Image = func(ctx context.Context) (string, error) {
		if a.cfg.worker != nil {
			image, err := a.cfg.worker.AgentImage(ctx)
			return image.Name, err
		}
		out, err := a.docker.Command(ctx, docker.Command{Args: []string{"image", "inspect", "--format", "{{.Id}}", a.cfg.agentImage}})
		if err != nil {
			return "", fmt.Errorf("reading the check agent image: %w", err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	return j
}

// Limits measured on a real-model fleet review of the demo (41 agent-judged
// checks): the largest session used about 93,000 tokens and 43 seconds. The
// caps leave room for larger repositories while stopping a session that loops.
// The token cap counts cumulative full prompts, including cache reads, across
// all turns.
const defaultCheckTimeCap = 3 * time.Minute
const defaultCheckTokenCap int64 = 250000

func checkEvaluatorFor(a *App) *verification.Evaluator {
	return verification.NewEvaluator(a.evaluator, a.cfg.clock.Now, a.cfg.stock.Tools).WithCheckJudge(a.checkJudge)
}

// Check tasks need the private runtime listener; AppTown's in-process Ask
// capability does not give a container a route to it.
type checkModelConfiguration struct {
	runtime piModelConfiguration
	enabled bool
}

func (m checkModelConfiguration) ModelsJSON(ctx context.Context) ([]byte, error) {
	if !m.enabled {
		return nil, fmt.Errorf("AI checks have no private model gateway configured. An administrator must configure the inference listener")
	}
	return m.runtime.ModelsJSON(ctx)
}
