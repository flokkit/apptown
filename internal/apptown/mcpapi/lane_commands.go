package mcpapi

import (
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

type LaneCommandsDependencies struct {
	Scope    scope
	Work     *workflow.Service
	Adoption *adoption.Service
	DB       *database.DB
	// Now is AppTown's clock, which stamps the tool's receipt (the real
	// clock when nil).
	Now func() time.Time
}
type laneCommands struct {
	scope    scope
	work     *workflow.Service
	adoption *adoption.Service
	db       *database.DB
	now      func() time.Time
}

func NewLaneCommands(deps LaneCommandsDependencies) []toolEntry {
	h := laneCommands{scope: deps.Scope, work: deps.Work, adoption: deps.Adoption, db: deps.DB, now: deps.Now}
	return []toolEntry{{Name: "create_change",
		order: 10, Description: "Opens a Change on a project in your name: a title and, in `intent`, what should be done and why. " + "With `lane` (a workflow the project can run, e.g. \"implement\"), AppTown's own agent starts on it at once; without, the Change is open and waiting for work.", InputSchema: schemaObject(map[string]any{"slug": slugArg, "title": schemaString("A short title for the Change, in plain words."), "intent": schemaString("What should be done and why — the brief the agent (or a person) works from."), "lane": schemaString("Optional: a lane to start on it now, e.g. \"implement\" or \"quick-change\" (get_project lists them)."), "type": schemaString("Optional: feature (default), fix, chore.")}, "slug", "title"),
		command: ownerCommand("lane_commands.createChange", h.createChange),
	},
		{
			Name:        "start_change_lane",
			order:       11,
			Description: "Starts a lane (a workflow the project can run, e.g. \"implement\") on an open Change: AppTown's own agent works from the Change's brief.",
			InputSchema: schemaObject(map[string]any{
				"id":   schemaID("The Change's id."),
				"lane": schemaString("The lane to start, e.g. \"implement\" (get_project lists them)."),
			}, "id", "lane"),
			command: ownerCommand("lane_commands.startChangeLane", h.startChangeLane),
		},
	}
}
func (h laneCommands) createChange(c *Call, args map[string]any) (any, error) {
	p, err := h.scope.projectForRead(c, argString(args, "slug"))
	if err != nil {
		return nil, err
	}
	title, intent, lane := argString(args, "title"), argString(args, "intent"), argString(args, "lane")
	typ := argString(args, "type")
	if typ == "" {
		typ = "feature"
	}
	if title == "" {
		return nil, refuse("give the Change a title")
	}
	authorityResource, err := h.scope.projects.AuthorityResource(c.Context, p.ID)
	if err != nil {
		return nil, err
	}
	if err := h.scope.authorize(c, "create_change", access.CapChangeCreate, authorityResource); err != nil {
		return nil, err
	}
	if lane != "" {
		if err := h.scope.authorize(c, "create_change", access.CapChangeInvokeLane, authorityResource); err != nil {
			return nil, err
		}
		if selected, err := h.adoption.LaneSelected(c.Context, p.ID, lane); err != nil {
			return nil, err
		} else if !selected {
			return nil, refuse("%s is not a lane %s can run; get_project lists them", lane, p.Name)
		}
	}
	var ch *workflow.Change
	var run *workflow.Run
	if lane != "" {
		ch, run, err = h.work.CreateAndRun(c.Context, workflow.InvokeChange{Change: workflow.ChangeDraft{ProjectID: p.ID, Type: typ, Title: title, Body: intent}, Workflow: lane})
	} else {
		ch, err = h.work.Create(c.Context, workflow.ChangeDraft{ProjectID: p.ID, Type: typ, Title: title, Body: intent})
	}
	if err != nil {
		return nil, refuse("the Change could not be opened: %v", err)
	}

	out := c.changeOut(ch, run)
	if run != nil {
		out["next"] = "the agent is working; get_change shows its progress and what it is waiting on"
	} else {
		out["next"] = "start_change_lane puts an agent on it, or checkout_change to work on it locally and push"
	}
	return out, nil
}
func (h laneCommands) startChangeLane(c *Call, args map[string]any) (any, error) {
	ch, p, err := h.scope.changeForWrite(h.work, c, args)
	if err != nil {
		return nil, err
	}
	lane := argString(args, "lane")
	if selected, err := h.adoption.LaneSelected(c.Context, p.ID, lane); err != nil {
		return nil, err
	} else if !selected {
		return nil, refuse("%s is not a lane %s can run; get_project lists them", lane, p.Name)
	}
	authorityResource, err := workflow.ChangeResource(c.Context, h.scope.projects, ch)
	if err != nil {
		return nil, err
	}
	if err := h.scope.authorize(c, "start_change_lane", access.CapChangeInvokeLane, authorityResource); err != nil {
		return nil, err
	}
	run, err := h.work.StartRun(c.Context, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: lane})
	if err != nil {
		return nil, refuse("the lane could not start: %v", err)
	}
	audit.At(clock.Or(h.now)()).BestEffort(c.Context, h.db, c.actor(), p.ID, "change.run-started", fmt.Sprintf("change %d run %d (%s)", ch.ID, run.ID, lane), map[string]any{"via": "mcp"})
	started, err := workflow.ReadChangeIn(c.Context, h.db, ch.ID)
	if err != nil {
		return nil, fmt.Errorf("the lane started (run %d), but Change %d could not be read again: %w", run.ID, ch.ID, err)
	}
	return c.changeOut(started, run), nil
}
