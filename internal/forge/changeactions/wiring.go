package changeactions

import (
	"context"
	"fmt"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/workflow"
)

// WiringMode chooses whether connection work starts in a chat or a workflow.
// Callers must parse external strings; the command also rejects constructed
// unknown values. Only an omitted external value selects the workflow default.
type WiringMode string

const (
	WiringChat     WiringMode = "chat"
	WiringWorkflow WiringMode = "workflow"
)

func ParseWiringMode(raw string) (WiringMode, error) {
	if raw == "" {
		return WiringWorkflow, nil
	}
	mode := WiringMode(raw)
	if err := mode.validate(); err != nil {
		return "", err
	}
	return mode, nil
}
func (m WiringMode) validate() error {
	switch m {
	case WiringChat, WiringWorkflow:
		return nil
	}
	return fault.Wrap(fault.Invalid, fmt.Errorf("mode must be chat or workflow"))
}

// Wiring asks for the Change that wires a project's attachment into its app.
type Wiring struct {
	Mode       WiringMode
	Capability string
	Draft      workflow.ChangeDraft
	Workflow   string // the workflow a WiringWorkflow run starts
	Who        string // receipt actor for the integration.wire record
}

// OpenWiring admits the wiring Change (and, in workflow mode, its first Run),
// links it to the attachment and records the integration.wire receipt in one
// transaction. Nothing prepares a workspace or dispatches until that commits,
// so a failed link leaves no Change, no Run and no receipt.
//
// While the attachment's linked Change is live, the command refuses with a
// conflict naming it (*integration.WireOpenError): a retry after a lost
// response finds the admitted work instead of adding a second. After commit
// the returned Change is linked even when an error follows: in workflow mode a
// failed preparation abandons it (a new wiring may then start) and a failed
// dispatch leaves it in progress; in chat mode a failed workspace preparation
// leaves it in progress with its workspace unprepared. See "Wiring an
// attachment" in design/subsystems/integration-runtime.md.
func (s *Service) OpenWiring(ctx context.Context, in Wiring) (*workflow.Change, *workflow.Run, error) {
	if err := in.Mode.validate(); err != nil {
		return nil, nil, err
	}
	link := func(ctx context.Context, tx *database.Tx, ch *workflow.Change) error {
		if err := integration.LinkWireIn(ctx, tx, ch.ProjectID, in.Capability, ch.ID); err != nil {
			return err
		}
		return audit.At(s.now()).RecordIn(ctx, tx, in.Who, ch.ProjectID, "integration.wire", in.Capability, map[string]any{"change": ch.ID, "mode": in.Mode})
	}
	if in.Mode == WiringChat {
		ch, err := s.work.CreateWith(ctx, in.Draft, link)
		return ch, nil, err
	}
	return s.work.CreateAndRun(ctx, workflow.InvokeChange{Change: in.Draft, Workflow: in.Workflow, Join: link})
}
