package workflowexec

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/ids"
)

// ModelRefusal is Work's admission question for a workflow's model steps
// (agent/pi and agent/review): "" when every one of them has a model,
// otherwise why not (model.NoModelRefusal). Each step's model is asked for as its execution asks
// (resolveAgentKnobs; the review's own model): the step's own, else the
// workflow's default, a catalog pin over both for an agent; then the
// project's slots and the instance default decide (model.Selection). A
// request that resolves to nothing is refused; a workflow with no model
// step is never refused.
func ModelRefusal(selection model.Selection) func(ctx context.Context, projectID ids.ID, spec *schema.Spec, inputsJSON string) string {
	return func(ctx context.Context, projectID ids.ID, spec *schema.Spec, inputsJSON string) string {
		if spec == nil {
			return ""
		}
		for _, stage := range spec.Stages {
			for _, step := range append(append([]schema.Step{}, stage.OnEnter...), stage.Steps...) {
				requested, _ := step.With["model"].(string)
				switch step.Uses {
				case AgentKind:
					requested = agentModelRequest(requested, spec, inputsJSON)
				case ReviewKind:
				default:
					continue
				}
				if pattern, _, _ := selection.Resolve(ctx, projectID, requested); pattern == "" {
					return model.NoModelRefusal
				}
			}
		}
		return ""
	}
}
