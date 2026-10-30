package workflowexec

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/gitexec"

	piharness "github.com/flokkit/apptown/internal/apptown/agentharness/pi"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// apptownEnv is the identity a run agent carries to reach AppTown: the
// complaints box and the CLI (apptown check, status)
// need the read-only listener's URL, the run, the step and a run token.
// APPTOWN_URL is the read-only listener — complaints POST is its sanctioned
// exception. The token is derived from the step attempt, so nothing revokes
// it: it stops working when the attempt is no longer live.
func (a *Executor) apptownEnv(ctx context.Context, run *workflow.Run, step *workflow.Step) (env []string) {
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
	env = append(env, a.apptownEnv(ctx, run, step)...)
	if k.useDind {
		// Implementation agents get what the APP containers get from the
		// project's attached integrations (the errors DSN, the feedback key,
		// the telemetry endpoint — values that ship to browsers and app
		// containers anyway, never the registry token: no step's Docker is
		// logged in to a registry). Without them an agent cannot fire the test hook it was told
		// to fire.
		given, err := a.integrations.Environment(ctx, run.ProjectID)
		if err != nil {
			return nil, "", err
		}
		env = append(env, given...)
	}
	if profile := string(cfg.Scm); profile != "" {
		if proj, err := project.ByIDIn(ctx, a.db, run.ProjectID); err == nil {
			repo, err := proj.Repository(a.cfg.DataDir)
			if err != nil {
				return nil, "", err
			}
			remote, _ := gitexec.Run(ctx, repo, "remote", "get-url", "origin")
			if scmEnv, why := a.integrations.SCMEnvironment(ctx, proj.ID, remote, profile); why != "" {
				a.progress.Append(step.ID, "scm access unavailable: "+why)
			} else {
				env = append(env, scmEnv...)
				scmGranted = profile
			}
		}
	}
	// HOME is pi's home, staged into the workspace before the steering
	// (agent.go), so the containerized pi finds providers without mounting
	// ~/.pi, and finds the guidance's skills there.
	env = append(env, "HOME=/work/.apptown/pi-home")
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
	// A refused declaration (cfg.needsRefused) starts none: the agent was
	// told why.
	if cfg.Needs && cfg.needsRefused == "" {
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
		dindEnv, dindNet, dindCleanup, derr := a.workshop.StartStep(ctx, step.ID, ws, network, piharness.DockerConfigDir)
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

// needsRefusal is the sentence an agent step is told when the catalogue
// refuses the app's declared needs (dependency.Refused) or does not hold
// them (dependency.UnknownNeeds); ok is false for any other outcome.
func needsRefusal(err error) (why string, ok bool) {
	var unknown *dependency.UnknownNeeds
	var refused *dependency.Refused
	switch {
	case errors.As(err, &unknown):
		return unknown.Error(), true
	case errors.As(err, &refused):
		return refused.Why, true
	}
	return "", false
}

// workspaceNeedsRefusal is why a local agent step starts no need services
// from the workspace's declaration, "" when it starts them or declares none
// (needsRefusal; a declaration that does not parse is said too). A catalogue
// read that fails is not a refusal: startAgentSandbox fails the step on it.
func (a *Executor) workspaceNeedsRefusal(ctx context.Context, ws string) string {
	c, err := docker.ReadWorkspaceContract(ws)
	switch {
	case errors.Is(err, docker.ErrNoContract):
		return ""
	case err != nil:
		return ".apptown/app.yaml cannot be read: " + err.Error()
	case len(c.Needs) == 0:
		return ""
	}
	why, err := a.dependencies.RefusesNeeds(ctx, c.Needs, c.NeedParams)
	if err != nil {
		return ""
	}
	return why
}
