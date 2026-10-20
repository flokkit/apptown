package workflowexec

import (
	"context"
	"fmt"
	"path/filepath"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/gitexec"

	piharness "flokkit.io/forge/internal/forge/agentharness/pi"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow"
)

// forgeEnv is the identity a run agent carries to reach AppTown: the
// complaints box and the CLI (apptown check, status)
// need the read-only listener's URL, the run, the step and a run token.
// APPTOWN_URL is the read-only listener — complaints POST is its sanctioned
// exception. The token is derived from the step attempt, so nothing revokes
// it: it stops working when the attempt is no longer live.
func (a *Executor) forgeEnv(ctx context.Context, run *workflow.Run, step *workflow.Step) (env []string) {
	runTok := a.runTok.Step(step.ID, step.Attempt)
	env = append(env, "APPTOWN_URL="+a.cfg.APIURL,
		fmt.Sprintf("APPTOWN_RUN_ID=%d", run.ID), "APPTOWN_STEP="+step.Name,
		"APPTOWN_SESSION_TOKEN="+runTok)
	if !run.ChangeID.IsZero() {
		// Lets `apptown check` resolve which change's gate checks to run without a
		// round-trip — the agent verifies against its own change before handoff.
		env = append(env, fmt.Sprintf("APPTOWN_CHANGE=%d", run.ChangeID))
	}
	if proj, err := project.ByIDIn(ctx, a.db, run.ProjectID); err == nil {
		env = append(env, "APPTOWN_PROJECT="+proj.Slug)
	}
	return env
}

// agentEnv is the environment an agent step runs with: step secrets, the
// AppTown identity (run token), the project's
// integration env for workshop agents, SCM access when granted, and the
// containerized pi-home shared by delegates.
func (a *Executor) agentEnv(ctx context.Context, run *workflow.Run, step *workflow.Step, cfg *agentConfig, ws, model string, k agentKnobs) (env []string, scmGranted string, err error) {
	if env, err = a.stepEnv(ctx, run.ProjectID); err != nil {
		return nil, "", err
	}
	env = append(env, a.forgeEnv(ctx, run, step)...)
	if k.useDind {
		// Implementation agents get what the APP containers get from the
		// project's attached integrations (the errors DSN, the feedback key,
		// the telemetry endpoint — values that ship to browsers and app
		// containers anyway, never the registry token, which the daemon
		// holds). Without them an agent cannot fire the test hook it was told
		// to fire.
		env = append(env, a.integrations.Environment(ctx, run.ProjectID)...)
	}
	if profile := string(cfg.Scm); profile != "" {
		if proj, err := project.ByIDIn(ctx, a.db, run.ProjectID); err == nil {
			remote, _ := gitexec.Run(ctx, filepath.Join(a.cfg.DataDir, "workspaces", "project-"+proj.Slug), "remote", "get-url", "origin")
			if scmEnv, why := a.integrations.SCMEnvironment(ctx, proj.ID, remote, profile); why != "" {
				a.progress.Append(step.ID, "scm access unavailable: "+why)
			} else {
				env = append(env, scmEnv...)
				scmGranted = profile
			}
		}
	}
	if k.executor == execution.Docker {
		// Stage pi's provider config into the workspace and point HOME at it,
		// so the containerized pi finds providers without mounting ~/.pi.
		if _, err := a.home.Stage(ctx, ws, run.ProjectID); err != nil {
			return nil, "", err
		}
		env = append(env, "HOME=/work/.apptown/pi-home")

	}
	return env, scmGranted, nil
}

// startAgentSandbox brings up what the agent's process runs against:
// contract-declared needs as emulators (process mode), declared sidecar
// services on a per-step network, and the private docker daemon. Returns the
// env extended with their connection details, the network to join, and one
// cleanup for all of it (non-nil even on error).
func (a *Executor) startAgentSandbox(ctx context.Context, run *workflow.Run, step *workflow.Step, cfg *agentConfig, ws string, env []string, k agentKnobs) (_ []string, network string, cleanup func(), err error) {
	var cleanups []func()
	cleanup = func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	// Process-mode needs (design/decisions/forge-native-contract.md): fulfill the app
	// contract's declared needs as emulators on this step's network and inject
	// their connection env, so the agent runs the app's processes against them.
	if cfg.Needs {
		if c, cerr := docker.ReadWorkspaceContract(ws); cerr == nil && len(c.Needs) > 0 {
			depSvcs, depEnv, err := (dependency.LocalRuntime{Catalog: a.dependencies, Steps: true}).Fulfil(ctx, c.Needs, c.NeedParams)
			if err != nil {
				return env, "", cleanup, err
			}
			cfg.Services = append(cfg.Services, depSvcs...)
			for key, v := range depEnv {
				env = append(env, key+"="+v)
			}
		}
	}
	if len(cfg.Services) > 0 {
		if k.executor != execution.Docker {
			return env, "", cleanup, fmt.Errorf("services require the docker executor")
		}
		var svcCleanup func()
		network, svcCleanup, err = docker.StartServices(ctx, *a.docker, fmt.Sprintf("%d", step.ID), cfg.Services)
		if err != nil {
			return env, "", cleanup, fmt.Errorf("starting services: %w", err)
		}
		cleanups = append(cleanups, svcCleanup)
		svcNames := make([]string, len(cfg.Services))
		for i, s := range cfg.Services {
			svcNames[i] = s.Name + " (" + s.Image + ")"
		}
		recordEvent(ctx, a.db, step.RunID, "services.started", map[string]any{"step": step.Name, "services": svcNames}, a.now())
	}
	if k.useDind {
		reg, err := a.stepRegistry(ctx, run.ProjectID)
		if err != nil {
			return env, "", cleanup, err
		}
		dindEnv, dindNet, dindCleanup, derr := a.workshop.StartStep(ctx, step.ID, ws, network, piharness.DockerConfigDir, reg)
		if derr != nil {
			// The workshop is a convenience, not a receipt — degrade honestly
			// rather than failing the step.
			a.progress.Append(step.ID, "private docker unavailable: "+derr.Error())
		} else {
			env = append(env, dindEnv...)
			network = dindNet
			cleanups = append(cleanups, dindCleanup)
			recordEvent(ctx, a.db, step.RunID, "docker.granted", map[string]any{"step": step.Name}, a.now())
		}
	}
	return env, network, cleanup, nil
}
