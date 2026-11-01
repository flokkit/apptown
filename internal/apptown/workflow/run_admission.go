package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
)

// definitionVersion is stored with every admitted Run declaration.
const definitionVersion = 1

type StartRequest struct {
	receiptActor string // verified receipt attribution, distinct from inference owner
	requestedBy  string // captured before dispatch; inherited by child/landing runs
	// owner is requestedBy as admitted (principal and way), when the caller
	// carried it; a root run records it rather than the name looked up.
	owner             access.Actor
	ProjectID         ids.ID
	ChangeID          ids.ID
	WorkflowName      string
	InputsJSON        string
	parentRunID       ids.ID
	parentStepID      ids.ID
	parentStepAttempt int64
	parentStepConfig  string
	acceptedRunID     ids.ID
	// landingBy is who admitted the run while holding change:land, set only
	// by Work from the verified caller (Service.landingBy); never a caller
	// input.
	landingBy string
	// evalAuthority is the eval's authority a run in one of its copies is
	// admitted with, set only by StartEvalRun (eval_authority.go).
	evalAuthority string
}

// IsChild reports whether workflow admitted this request from a parent run.
func (in StartRequest) IsChild() bool { return !in.parentRunID.IsZero() }

type DefinitionRef struct {
	Name    string
	Version int
	Origin  string
}
type StartEvent struct {
	Kind    string
	Payload json.RawMessage
}
type PreparedRun struct {
	Workflow   DefinitionRef
	Spec       schema.Spec
	AdoptionID ids.ID
	Events     []StartEvent
}

// runAdmissions is for already-admitted execution and named transactional
// participation. Caller-facing authority belongs to Service.
type runAdmissions struct{ records runStartStore }

func newRunAdmissions(db *database.DB, stages Stages, chats Presence) runAdmissions {
	return runAdmissions{runStartStore{db: db, stages: stages, chats: chats}}
}
func (s runAdmissions) Create(ctx context.Context, in StartRequest, definition PreparedRun) (*Run, error) {
	return s.records.create(ctx, in, definition)
}
func (s runAdmissions) CreateIn(ctx context.Context, tx *database.Tx, in StartRequest, definition PreparedRun) (*Run, error) {
	return s.records.createIn(ctx, tx, in, definition)
}
func (s runAdmissions) Prepared(ctx context.Context, id ids.ID) error {
	return s.records.prepared(ctx, id)
}
func validateStart(in StartRequest) error {
	if in.ProjectID.IsZero() || in.WorkflowName == "" || (in.parentRunID.IsZero() && !in.parentStepID.IsZero()) || (!in.acceptedRunID.IsZero() && !in.parentRunID.IsZero()) {
		return fmt.Errorf("a run needs a project, workflow, and a valid parent context")
	}
	var inputs map[string]json.RawMessage
	if err := json.Unmarshal([]byte(in.InputsJSON), &inputs); err != nil || inputs == nil {
		return fmt.Errorf("run inputs must be a JSON object")
	}
	return nil
}
func encodeRunDefinition(in PreparedRun) ([]byte, error) {
	if len(in.Spec.Stages) == 0 || in.Spec.Stages[0].Name == "" {
		return nil, fmt.Errorf("workflow %q has no initial stage", in.Workflow.Name)
	}
	for _, event := range in.Events {
		if event.Kind == "" || !json.Valid(event.Payload) {
			return nil, fmt.Errorf("invalid prepared Run receipt")
		}
	}
	spec := in.Spec
	spec.V = definitionVersion
	return json.Marshal(spec)
}
func requireAttemptWrite(n int64, err error) error {
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrStepAttemptLost
	}
	return nil
}

type runCreatedReceipt struct {
	Workflow string `json:"workflow"`
	Version  int    `json:"version"`
	Origin   string `json:"origin"`
}
type changeLandingReceipt struct {
	Change ids.ID `json:"change"`
}
type calledRunEvidence struct {
	ChildRunID ids.ID `json:"childRunId"`
	// ChildRunNumber names the child to people; evidence written before
	// runs were numbered does not carry it.
	ChildRunNumber  int64  `json:"childRunNumber"`
	Workflow        string `json:"workflow"`
	WorkflowVersion int    `json:"workflowVersion"`
	StartedAtMS     int64  `json:"startedAtMs"`
}
type runCalledReceipt struct {
	Step       string `json:"step"`
	Workflow   string `json:"workflow"`
	Version    int    `json:"version"`
	ChildRunID ids.ID `json:"childRunId"`
}

func requireRunWrite(n int64, err error) error {
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("required Run admission record was not written")
	}
	return nil
}

// Freeze extension maps and receipt bytes before admission can wait on locks.
// The exact same frozen declaration produces both the ledger and first stage.
func freezeRunDefinition(in PreparedRun) (PreparedRun, []byte, error) {
	encoded, err := encodeRunDefinition(in)
	if err != nil {
		return PreparedRun{}, nil, err
	}
	frozen := in
	frozen.Spec = schema.Spec{}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&frozen.Spec); err != nil {
		return PreparedRun{}, nil, err
	}
	frozen.Events = make([]StartEvent, len(in.Events))
	for i, event := range in.Events {
		frozen.Events[i] = StartEvent{Kind: event.Kind, Payload: append(json.RawMessage(nil), event.Payload...)}
	}
	return frozen, encoded, nil
}
