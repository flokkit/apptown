package mcpapi

import (
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/evals"
)

// EvalReadsDependencies binds the eval read to its owner.
type EvalReadsDependencies struct {
	Evals *evals.Service
}

// NewEvalReads is the eval read: an eval's scorecard, under the permission its page takes, asked of the
// caller. Agents read; starting, continuing and deleting an eval stay with
// people.
func NewEvalReads(deps EvalReadsDependencies) []toolEntry {
	h := evalReads{evals: deps.Evals}
	return []toolEntry{{
		Name:  "get_eval",
		order: 23,
		Description: "Experimental. Returns an eval's scorecard: landed Changes re-run in throwaway copies as their projects run now and with one thing varied (a model profile or a pack version), " +
			"each arm's checks compared with the Change's base finding by finding (new, fixed, already there, or not comparable with why), and the variant against the baseline measure by measure. " +
			"A person reads it; it decides nothing and is never evidence. Give its id, or a pack and version for that version's newest eval.",
		InputSchema: schemaObject(map[string]any{"id": schemaID("The eval's id."), "pack": schemaString("A pack's name, with version."), "version": schemaInt("The pack version.")}),
		command:     ownerCommand("evals.getEval", h.getEval),
	}}
}

type evalReads struct{ evals *evals.Service }

func (h evalReads) getEval(c *Call, args map[string]any) (any, error) {
	if _, ok := args["id"]; ok {
		id, err := argID(args, "id")
		if err != nil {
			return nil, err
		}
		return h.evals.Scorecard(c.Context, id)
	}
	pack := argString(args, "pack")
	version, err := argNumber(args, "version")
	if pack == "" || err != nil {
		return nil, refuse("give an eval id, or a pack and a version")
	}
	subject := fmt.Sprintf("%s@%d", pack, version)
	e, err := h.evals.LatestOf(c.Context, evals.SubjectPack, subject)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return map[string]any{"eval": nil, "note": "no eval has run on " + subject}, nil
	}
	return e, nil
}
