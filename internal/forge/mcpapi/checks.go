package mcpapi

import (
	"context"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/forge/workflow"
)

type ChecksDependencies struct {
	Scope        Scope
	Work         *workflow.Service
	Verification interface {
		Check(ctx context.Context, ch *workflow.Change, sel verification.Selection) (map[string]any, error)
	}
}
type checks struct {
	scope        Scope
	work         *workflow.Service
	verification interface {
		Check(ctx context.Context, ch *workflow.Change, sel verification.Selection) (map[string]any, error)
	}
}

func NewChecks(deps ChecksDependencies) []Tool {
	h := checks{scope: deps.Scope, work: deps.Work, verification: deps.Verification}
	return []Tool{{Name: "run_change_checks",
		order: 12, Description: "Runs a Change's checks now — the same guidance checks, tool checks and source scans its landing will apply — and returns each result. Slow (a minute or more); the checks build and scan the committed tree.", InputSchema: schemaObject(map[string]any{"id": schemaID("The Change's id.")}, "id"), command: ownerCommand("checks.runChangeChecks", h.runChangeChecks)}}
}
func (h checks) runChangeChecks(c *Call, args map[string]any) (any, error) {
	ch, _, err := h.scope.changeForWrite(h.work, c, args)
	if err != nil {
		return nil, err
	}
	if err := h.scope.authorize(c, "run_change_checks", access.CapChangeRunChecks, workflow.ChangeResource(c.Context, h.scope.projects, ch)); err != nil {
		return nil, err
	}
	out, err := h.verification.Check(c.Context, ch, verification.Selection{}) // the change's latest commit on the forge (a chat on a worker saving first); the answer names it
	if err != nil {
		return nil, refuse("%v", err)
	}
	out["changeId"] = ch.ID
	out["webUrl"] = c.changeURL(ch.ID)
	return out, nil
}
