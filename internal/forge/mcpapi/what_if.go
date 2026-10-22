package mcpapi

import (
	"fmt"

	"flokkit.io/forge/internal/forge/conformance"
)

// WhatIfReadsDependencies binds the what-if read to its owner.
type WhatIfReadsDependencies struct {
	Conformance *conformance.Service
}

// NewWhatIfReads is the what-if read (design/subsystems/conformance.md):
// what a pack version's checks would say about projects' main. It is
// hypothetical, never a project's standing. Agents read it; starting one
// and moving a recommendation stay with people and the API.
func NewWhatIfReads(deps WhatIfReadsDependencies) []Tool {
	h := whatIfReads{conformance: deps.Conformance}
	return []Tool{{
		Name:  "get_what_if",
		order: 22,
		Description: "Experimental. Returns a what-if: what a pack version's checks would say about projects' committed main beside the checks they adopted, for projects on the pack or joining it. " +
			"Each check row is would-fail, would-pass, unchanged, not-assessed (with the reason it was not judged) or no-longer-required, with the brief a fix would start from and, when at least three past fixes exist, what they cost; " +
			"each rule the version moves back is a row of its own, older-version or removed, with no check. Each project has a score, its unjudged checks grouped by reason with what would make them assessable, and what moves back; fleet groups unjudged checks across projects. " +
			"Hypothetical: never a project's standing. Give its id, or a pack and version for that version's newest what-if.",
		InputSchema: schemaObject(map[string]any{"id": schemaID("The what-if's id."), "pack": schemaString("A pack's name, with version."), "version": schemaInt("The pack version.")}),
		command:     ownerCommand("what_if.getWhatIf", h.getWhatIf),
	}}
}

type whatIfReads struct{ conformance *conformance.Service }

func (h whatIfReads) getWhatIf(c *Call, args map[string]any) (any, error) {
	if _, ok := args["id"]; ok {
		id, err := argID(args, "id")
		if err != nil {
			return nil, err
		}
		return h.conformance.WhatIf(c.Context, id)
	}
	pack := argString(args, "pack")
	version, err := argNumber(args, "version")
	if pack == "" || err != nil {
		return nil, refuse("give a what-if id, or a pack and a version")
	}
	w, err := h.conformance.LatestWhatIf(c.Context, pack, version)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return map[string]any{"whatIf": nil, "note": fmt.Sprintf("no what-if has run on %s v%d", pack, version)}, nil
	}
	return w, nil
}
