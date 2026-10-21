package workflowexec

import (
	"flokkit.io/forge/internal/execution"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/forge/wow"
)

type agentKnobs struct {
	model, thinking, executor, image string
	useDind                          bool
}

// agentModelRequest is the model an agent step asks for, before slot
// resolution: the step's own, else the workflow's default; the definition's
// catalog pin wins over both (an owner decision: the pin is part of the
// evaluated definition). Run admission asks the same (ModelRefusal).
func agentModelRequest(stepModel string, spec *schema.Spec, inputsJSON string) string {
	model := stepModel
	if model == "" && spec != nil {
		model = spec.Defaults.Model
	}
	if ck := wow.CatalogKnobs(inputsJSON); ck != nil && ck.Model != "" {
		model = ck.Model
	}
	return model
}

func (a *Executor) resolveAgentKnobs(run *workflow.Run, cfg *agentConfig) agentKnobs {
	k := agentKnobs{thinking: cfg.Thinking, executor: cfg.Executor, image: cfg.Image}
	spec, _ := run.Definition()
	k.model = agentModelRequest(cfg.Model, spec, run.InputsJSON)
	if spec != nil {
		if k.thinking == "" {
			k.thinking = spec.Defaults.Thinking
		}
		if k.executor == "" {
			k.executor = spec.Defaults.Executor
		}
		if k.image == "" {
			k.image = spec.Defaults.Image
		}
	}
	// Change-catalog knobs (design/decisions/change-catalog.md): besides the
	// model pin (agentModelRequest), thinking and timeout are the whitelisted
	// effort knobs; gates are untouchable.
	if ck := wow.CatalogKnobs(run.InputsJSON); ck != nil {
		if ck.Thinking != "" {
			k.thinking = ck.Thinking
		}
		if ck.TimeoutMinutes > 0 {
			cfg.TimeoutMinutes = ck.TimeoutMinutes
		}
	}
	// No explicit model on the step: leave empty so slot resolution walks
	// default slot -> fallbacks -> the -agent-model flag LAST.
	if k.executor == "" {
		k.executor = a.cfg.Backend
	}
	if k.image == "" {
		k.image = a.cfg.Image
	}
	// Private Docker (the agent's workshop) is what the step declares
	// (docker: true), for a Docker-executed agent only.
	k.useDind = k.executor == execution.Docker && cfg.Docker
	return k
}

// dockerRefusal is why the step is not started, before any effect: a step of
// a run admitted under the old private-Docker rule, or private Docker
// declared on an AppTown started with -no-private-docker.
func (a *Executor) dockerRefusal(step *workflow.Step, k agentKnobs) string {
	switch {
	case workflow.StartedUnderOldDockerRule(step.Config):
		return workflow.OldDockerRuleRefusal
	case k.useDind && a.cfg.NoPrivateDocker:
		return "this step declares private Docker (docker: true), and this AppTown was started with -no-private-docker"
	}
	return ""
}

func hasSkill(cfg *agentConfig, name string) bool {
	for _, s := range cfg.Skills {
		if s == name {
			return true
		}
	}
	return false
}

// steeringFile is the assembled system-prompt appendix: its text (written to
// .apptown/steering.md and attached as the "steering" artifact), its hash, and
// what went into it, for evidence.
