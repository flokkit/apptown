package workflow

import "encoding/json"

// Placement is where a step ran: a worker, by its address (and, for the
// check worker, the check role it reported at the forge's start), or this
// machine (design/operations/workers.md, "A check worker"). Work records it on every
// check and script result, as a protected field of the evidence the runner
// cannot set. Results recorded before placement was recorded have none:
// unknown.
type Placement struct {
	Worker string `json:"worker,omitempty"`
	Role   string `json:"role,omitempty"`
	Local  bool   `json:"local,omitempty"`
}

// checkKinds are the step types that run on the check executor.
var checkKinds = []string{"tests", "scan", "guidance/conformance"}

// The gate's words for a passing result that cannot satisfy it while the
// forge has a check worker.
const (
	notIndependent = " (not independently checked; rerun required)"
	unreadable     = " (its evidence could not be read; rerun required)"
	scriptNotCheck = " (scripts run beside agents, so they are not independent checks while a check worker is configured; express this check as a tests or guidance step)"
)

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

// refuses says why a step's result cannot satisfy a gate, or "" if it can.
// Once the forge has a check worker (p is its placement), a passing check
// result counts only if that worker produced it: one from an agent worker,
// from this machine, of unknown placement, or recorded before the check
// worker was configured needs a fresh run there. A script never counts: it
// runs on the agent worker or this machine, wherever it ran. The original is
// kept as it is. A person's override is an exception, not a check result,
// and is judged as before; so is everything without a check worker.
func (p Placement) refuses(s *Step) string {
	if p.Role != "check" || s.Status != StatusPass || s.OverrideBy != "" {
		return ""
	}
	if s.Type == "script" {
		return scriptNotCheck
	}
	if !isCheckKind(s.Type) {
		return ""
	}
	ev, err := ReadCheckEvidence(s.Evidence)
	if err != nil {
		return unreadable
	}
	if ev.Placement != nil && *ev.Placement == p {
		return ""
	}
	return notIndependent
}
