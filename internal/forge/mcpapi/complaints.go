package mcpapi

import (
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/complaint"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/strs"
)

type ComplaintsDependencies struct {
	Scope      Scope
	Work       *workflow.Service
	Complaints *complaint.Box
}
type complaints struct {
	scope      Scope
	work       *workflow.Service
	complaints *complaint.Box
}

func NewComplaints(deps ComplaintsDependencies) []Tool {
	h := complaints{scope: deps.Scope, work: deps.Work, complaints: deps.Complaints}
	return []Tool{{Name: "file_complaint",
		order: 20, Description: "Files a complaint about AppTown itself: a standard tool that failed, a documented promise that did not hold, something you had to guess or work around. " + "Platform friction only — problems in the code you are working on belong in the Change. Filing triggers nothing; a person reads it.", InputSchema: schemaObject(map[string]any{"text": schemaString("What happened, in a sentence or two."), "why": schemaString("Why it slowed you down (optional, a sentence)."), "wish": schemaString("What you wish had been true (optional, a sentence)."), "slug": schemaString("Optional: the project you were working on."), "change_id": schemaID("Optional: the Change you were working on."), "about": schemaString("Optional: what it is about. forge (default): AppTown itself got in the way. way-of-working: the project's adopted ways of working (its packs' guidance, checks or workflows) asked for something wrong or unworkable. project: the project's own code, setup or guidance.")}, "text"), command: publicCommand("complaints.fileComplaint", h.fileComplaint)}}
}
func (h complaints) fileComplaint(c *Call, args map[string]any) (any, error) {
	text := strings.TrimSpace(argString(args, "text"))
	if text == "" {
		return nil, refuse("say what happened")
	}
	if len(text) > 2000 {
		text = strs.Truncate(text, 2000)
	}
	var projectID, changeID ids.ID
	if slug := argString(args, "slug"); slug != "" {
		p, err := h.scope.projectForRead(c, slug)
		if err != nil {
			return nil, err
		}
		projectID = p.ID
	}
	if _, ok := args["change_id"]; ok {
		ch, p, err := h.scope.changeForRead(h.work, c, map[string]any{"id": args["change_id"]})
		if err != nil {
			return nil, err
		}
		changeID, projectID = ch.ID, p.ID
	}
	id, err := h.complaints.File(c.Context, complaint.Entry{ProjectID: projectID, ChangeID: changeID, Source: "mcp",
		Actor: c.actor(), Text: text, Why: argString(args, "why"), Wish: argString(args, "wish"), About: argString(args, "about")})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "webUrl": c.BaseURL + "/complaints",
		"next": "thank you; a person reads these. Carry on with the work."}, nil
}
