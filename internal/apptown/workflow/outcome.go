package workflow

import "encoding/json"

// Runner-owned extension payloads are encoded once at the adapter boundary.
func EncodeOutcome(status StepState, evidence any, extra map[string]any) (StepOutcome, error) {
	body, err := json.Marshal(evidence)
	if err != nil {
		return StepOutcome{}, err
	}
	details, err := json.Marshal(extra)
	if err != nil {
		return StepOutcome{}, err
	}
	return StepOutcome{Status: status, Evidence: body, Details: details}, nil
}
