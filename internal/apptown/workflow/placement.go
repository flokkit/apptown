package workflow

import "encoding/json"

// Placement is where a step ran: a worker, by its address, or this machine.
// Work records it on every check and script result, as a protected field of
// the evidence the runner cannot set. Results recorded before placement was
// recorded have none: unknown.
type Placement struct {
	Worker string `json:"worker,omitempty"`
	Local  bool   `json:"local,omitempty"`
}

// placed records p on a check or script step's evidence, replacing any the
// runner put there.
func (p Placement) placed(kind string, evidence json.RawMessage) (json.RawMessage, error) {
	if p == (Placement{}) || !carriesCheckEvidence(kind) {
		return evidence, nil
	}
	var fields map[string]json.RawMessage
	if len(evidence) > 0 {
		if err := json.Unmarshal(evidence, &fields); err != nil {
			return nil, err
		}
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	var err error
	if fields["placement"], err = json.Marshal(p); err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}
