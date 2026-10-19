package mcpapi

import (
	"flokkit.io/forge/internal/forge/operations"
)

// OperationReadsDependencies binds the operation read to its owner.
type OperationReadsDependencies struct {
	Operations *operations.Service
}

// NewOperationReads is the operation read (internal/forge/operations):
// how a long action someone started (a copy, a check, a take-down) stands.
// A tool that starts one answers its id; this follows it. The owner
// decides who may read it; one the caller may not read is missing.
func NewOperationReads(deps OperationReadsDependencies) []Tool {
	h := operationReads{ops: deps.Operations}
	return []Tool{{
		Name:  "get_operation",
		order: 24,
		Description: "Returns an operation: a long action someone asked for, such as a data copy, a restore check or a take-down. " +
			"Its state is queued, running, succeeded, failed, unknown or cancelled; verification (verified or not-verified) is a separate fact from success. " +
			"Unknown means AppTown stopped while it ran and whether its effect happened is not known: look before asking again. Steps say what happened, in order.",
		InputSchema: schemaObject(map[string]any{"id": schemaID("The operation's id.")}, "id"),
		command:     ownerCommand("operations.getOperation", h.getOperation),
	}}
}

type operationReads struct{ ops *operations.Service }

func (h operationReads) getOperation(c *Call, args map[string]any) (any, error) {
	id, err := argID(args, "id")
	if err != nil {
		return nil, err
	}
	return h.ops.Get(c.Context, id)
}
