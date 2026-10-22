package mcpapi

import (
	"fmt"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/changeactions"
	"flokkit.io/forge/internal/forge/workflow"
)

type WorkCommandsDependencies struct {
	Scope   Scope
	Work    *workflow.Service
	Actions *changeactions.Service
	DB      *database.DB
}
type workCommands struct {
	scope   Scope
	work    *workflow.Service
	actions *changeactions.Service
	db      *database.DB
}

func NewWorkCommands(deps WorkCommandsDependencies) []Tool {
	h := workCommands{scope: deps.Scope, work: deps.Work, actions: deps.Actions, db: deps.DB}
	return []Tool{{Name: "request_change_landing",
		order: 14, Description: "Hands an in-progress Change off for approval: freezes its current revision and starts the landing attempt (gates, then the project's approvers). " + "You cannot approve or land it yourself; the person does that in AppTown.", InputSchema: schemaObject(map[string]any{"id": schemaID("The Change's id.")}, "id"), command: ownerCommand("work_commands.requestChangeLanding", h.requestChangeLanding)}, {Name: "send_change_back",
		order: 15, Description: "Sends a Change back to the agent that worked on it with a note saying what to change. The agent continues from where it was.", InputSchema: schemaObject(map[string]any{"id": schemaID("The Change's id."), "note": schemaString("What to change, in plain words — this is what the agent works from.")}, "id", "note"),
		command: ownerCommand("work_commands.sendChangeBack", h.sendChangeBack),
	},
		{
			Name:        "abandon_change",
			order:       16,
			Description: "Closes a Change that will not continue. Nothing is deleted: the branch and the record stay; its workspace goes.",
			InputSchema: schemaObject(map[string]any{
				"id":     schemaID("The Change's id."),
				"reason": schemaString("Why, in a sentence."),
			}, "id", "reason"),
			command: ownerCommand("work_commands.abandonChange", h.abandonChange),
		},
	}
}
func (h workCommands) requestChangeLanding(c *Call, args map[string]any) (any, error) {
	ch, p, err := h.scope.changeForWrite(h.work, c, args)
	if err != nil {
		return nil, err
	}
	if ch.State != workflow.ChangeInProgress {
		return nil, refuse("Change %d is %s; only an in-progress Change can be handed off", ch.ID, ch.State)
	}
	if err := h.scope.authorize(c, "request_change_landing", access.CapChangeRequestLand, workflow.ChangeResource(c.Context, h.scope.projects, ch)); err != nil {
		return nil, err
	}
	run, err := h.actions.RequestLanding(c.Context, p, ch, c.actor())
	if err != nil {
		return nil, refuse("the landing could not start: %v", err)
	}
	landing, err := workflow.ReadChangeIn(c.Context, h.db, ch.ID)
	if err != nil {
		return nil, fmt.Errorf("the landing started (run %d), but Change %d could not be read again: %w", run.ID, ch.ID, err)
	}
	out := c.changeOut(landing, run)
	out["next"] = "the gates are running; when they pass, the approvers see it. get_change shows where it is."
	return out, nil
}
func (h workCommands) sendChangeBack(c *Call, args map[string]any) (any, error) {
	ch, _, err := h.scope.changeForWrite(h.work, c, args)
	if err != nil {
		return nil, err
	}
	note := argString(args, "note")
	if note == "" {
		return nil, refuse("say what to change — the note is what the agent works from")
	}
	if err := h.scope.authorize(c, "send_change_back", access.CapChangeSendBack, workflow.ChangeResource(c.Context, h.scope.projects, ch)); err != nil {
		return nil, err
	}
	result, err := h.work.SendBack(c.Context, workflow.SendBack{ChangeID: ch.ID, Note: note})
	runID, stepID := result.RunID, result.StepID
	if err != nil {
		return nil, refuse("%v", err)
	}
	next := "the agent is reworking it; get_change shows its progress"
	if runID.IsZero() {
		next = "no agent works on this change: its landing stopped and it is back in progress; push the fix to its branch, then land it again"
	}
	return map[string]any{"changeId": ch.ID, "runId": runID, "stepId": stepID, "webUrl": c.changeURL(ch.ID), "next": next}, nil
}
func (h workCommands) abandonChange(c *Call, args map[string]any) (any, error) {
	ch, _, err := h.scope.changeForWrite(h.work, c, args)
	if err != nil {
		return nil, err
	}
	if err := h.scope.authorize(c, "abandon_change", access.CapChangeAbandon, workflow.ChangeResource(c.Context, h.scope.projects, ch)); err != nil {
		return nil, err
	}
	if _, err := h.work.Abandon(c.Context, workflow.StopWork{ChangeID: ch.ID, Reason: argString(args, "reason")}); err != nil {
		return nil, err
	}
	abandoned, err := workflow.ReadChangeIn(c.Context, h.db, ch.ID)
	if err != nil {
		return nil, fmt.Errorf("Change %d is abandoned, but it could not be read again: %w", ch.ID, err)
	}
	return c.changeOut(abandoned, nil), nil
}
