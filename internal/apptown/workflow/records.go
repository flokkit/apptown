package workflow

import (
	"encoding/json"
	"github.com/flokkit/apptown/internal/apptown/status"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/stamp"
)

// Run is a workflow execution pinned to its definition. RetryRevision fences
// decisions made before changed work; it is distinct from a step's attempt.
type Run struct {
	StatusLabel       status.DisplayStatus `json:"statusLabel"`
	ParentStepAttempt int64                `json:"-"`
	RetryRevision     int64                `json:"-"` // changed work invalidates an earlier advancement read
	ID                ids.ID               `json:"id"`
	Number            int64                `json:"number"` // within its project
	ProjectID         ids.ID               `json:"projectId"`
	WorkflowName      string               `json:"workflowName"`
	WorkflowVersion   int                  `json:"workflowVersion"`
	Kind              string               `json:"kind"`
	Status            RunState             `json:"status"`
	CurrentStage      string               `json:"currentStage"`
	DefinitionJSON    string               `json:"-"`
	ChangeID          ids.ID               `json:"changeId"`
	ParentRunID       ids.ID               `json:"parentRunId,omitzero"`
	ParentStepID      ids.ID               `json:"parentStepId,omitzero"`
	InputsJSON        string               `json:"-"`
	AdoptionID        ids.ID               `json:"adoptionId,omitzero"` // the revision this run reads (§4.1); 0 = before receipts
	CreatedAt         string               `json:"createdAt"`
	UpdatedAt         string               `json:"updatedAt"`
}

type Step struct {
	runEnded    bool
	StatusLabel status.DisplayStatus `json:"statusLabel"`

	Attempt                                 int64 `json:"-"`
	ownedStepID, ownedRunID, ownedProjectID ids.ID
	ownedAttempt                            int64           // only assigned by committed admission, never by a read
	ownedSourceGeneration                   int64           // the branch authority the attempt was admitted under
	ID                                      ids.ID          `json:"id"`
	RunID                                   ids.ID          `json:"runId"`
	Stage                                   string          `json:"stage"`
	Name                                    string          `json:"name"`
	Type                                    string          `json:"type"`
	Config                                  json.RawMessage `json:"config"`
	Status                                  StepState       `json:"status"`
	Evidence                                json.RawMessage `json:"evidence"`
	OverrideBy                              string          `json:"overrideBy"`
	OverrideReason                          string          `json:"overrideReason"`
	StartedAt                               stamp.Time      `json:"startedAt"`
	FinishedAt                              stamp.Time      `json:"finishedAt"`
	// CheckReading is set only by the run snapshot, for the page: nil means
	// no reading was made, not that the evidence was empty or readable. Go
	// readers call ReadCheckEvidence on Evidence.
	CheckReading *CheckReading `json:"checkReading,omitempty"`
}

// OwnedAttempt is observation only: reading a Step never grants this marker.
func (s Step) OwnedAttempt() int64 {
	if s.ID != s.ownedStepID || s.RunID != s.ownedRunID {
		return 0
	}
	return s.ownedAttempt
}

// SourceGeneration is the branch authority this attempt was admitted under,
// the grant its publications present. Reading a Step never grants it.
func (s Step) SourceGeneration() int64 {
	if s.ID != s.ownedStepID || s.RunID != s.ownedRunID {
		return 0
	}
	return s.ownedSourceGeneration
}

func (s Step) MarshalJSON() ([]byte, error) {
	type view Step
	out := view(s)
	out.StatusLabel = s.displayLabel()
	return json.Marshal(out)
}
