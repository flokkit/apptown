package forge

import "flokkit.io/forge/internal/forge/workflowexec"

func agentExecutionFor(a *App, process workflowexec.Process) *workflowexec.Executor {
	// With a worker, agent steps run on it, through the task runner scripts
	// use, and nowhere else; review steps are refused there until they move
	// too (plan step 6). Without one nothing changes.
	var worker *workflowexec.AgentWorker
	if a.cfg.worker != nil {
		worker = &workflowexec.AgentWorker{Tasks: a.tasks}
	}
	return workflowexec.New(a.DB, workflowexec.Dependencies{
		Adoption: a.Adoption, Integrations: a.Integrations, Secrets: a.Secrets, Catalog: a.Dependencies, Models: a.modelSelection, Capacity: a.capacity, Progress: &a.progress, Tokens: &a.runTok, Docker: &a.docker, Workshop: a.workshop, Instructions: a.steering, Home: piHomeFor(a), CLI: a.cli, Process: process, Ledger: a.Usage.Ledger, Publisher: repositoryPublisher{secrets: a.Secrets, db: a.DB, dataDir: a.cfg.dataDir, now: a.cfg.clock.Now}, Branches: a.branches, Panics: jobReporter{},
		Worker: worker,
		Tracer: a.telemetry.tracer, Meter: a.telemetry.meter, Links: a.telemetry.links, Now: a.cfg.clock.Now,
	}, workflowexec.Config{DataDir: a.cfg.dataDir, Image: a.cfg.agentImage, Backend: a.cfg.agentExecutor, APIURL: a.cfg.consoleAPIURL, NoPrivateDocker: a.cfg.noPrivateDocker, PrivateDockerOnWorker: a.cfg.privateDocker})
}
