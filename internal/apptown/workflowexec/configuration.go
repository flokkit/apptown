package workflowexec

import (
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
)

type agentKnobs struct {
	model, thinking, image string
	useDind                bool
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
	k := agentKnobs{thinking: cfg.Thinking, image: cfg.Image}
	spec, _ := run.Definition()
	k.model = agentModelRequest(cfg.Model, spec, run.InputsJSON)
	if spec != nil {
		if k.thinking == "" {
			k.thinking = spec.Defaults.Thinking
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
	// default slot -> fallbacks.
	if k.image == "" {
		k.image = a.cfg.Image
	}
	// Private Docker (the agent's workshop) is what the step declares
	// (docker: true).
	k.useDind = cfg.Docker
	return k
}

// executorRefusal is why a step whose admitted definition names an executor
// other than docker (accepted before host processes were removed) is not
// started: it is refused, never run elsewhere than a container.
func executorRefusal(run *workflow.Run, cfg *agentConfig) string {
	executor := cfg.Executor
	if spec, _ := run.Definition(); spec != nil && executor == "" {
		executor = spec.Defaults.Executor
	}
	if err := schema.CheckExecutor(executor); err != nil {
		return err.Error()
	}
	return ""
}

// dockerRefusal is why the step is not started, before any effect: private
// Docker declared on an AppTown started with -private-docker=false.
func (a *Executor) dockerRefusal(k agentKnobs) string {
	if k.useDind && !a.cfg.PrivateDocker {
		return privateDockerOff
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
