package workflow

import "github.com/flokkit/apptown/internal/apptown/status"

func (s Step) displayLabel() status.DisplayStatus {
	if s.OverrideBy != "" {
		return status.DisplayExcepted
	}
	if s.CheckReading != nil && s.CheckReading.Unreadable != "" {
		return status.DisplayUnknown
	}
	if s.runEnded && (s.Status == StatusPending || s.Status == StatusRunning) {
		return status.DisplayStopped
	}
	switch s.Status {
	case StatusPass:
		// A gate may allow existing failures, but its rules are still unmet.
		if s.Type == "guidance/conformance" && s.CheckReading != nil {
			for _, check := range s.CheckReading.Checks {
				if check.Verdict == VerdictFail && (check.Gates == nil || *check.Gates) {
					return status.DisplayFailed
				}
			}
		}
		// A check that could not be assessed gave no answer, gating or not:
		// the step's pass is its gate's outcome, not a claim that it passed.
		if s.CheckReading.unassessed() {
			return status.DisplayUnknown
		}
		return status.DisplayPassed
	case StatusFail:
		return status.DisplayFailed
	case StatusError:
		return status.DisplayUnknown
	case StatusRunning:
		return status.DisplayWorking
	case StatusNeedsHuman:
		return status.DisplayWaitingForYou
	case StatusPending:
		if s.Type == "approval" && s.approvalReady {
			return status.DisplayWaitingForApproval
		}
		return status.DisplayWaitingForChecks
	case StatusSkipped:
		return status.DisplayStopped
	case StatusNotApplicable:
		return status.DisplayNotApplicable
	default:
		return status.DisplayUnknown
	}
}

// display describes execution progress without claiming a runtime is running.
func (s RunState) display() status.DisplayStatus {
	switch s {
	case RunRunning:
		return status.DisplayWorking
	case RunDone:
		return status.DisplayFinished
	case RunAbandoned:
		return status.DisplayAbandoned
	default:
		return status.DisplayUnknown
	}
}

// display keeps failures on main and undecided findings out of the current change's failures.
func (c CheckReceipt) display() status.DisplayStatus {
	switch c.Verdict {
	case VerdictPass:
		return status.DisplayPassed
	case VerdictExcepted:
		return status.DisplayExcepted
	case VerdictNotApplicable:
		return status.DisplayNotApplicable
	case VerdictSkipped:
		return status.DisplayStopped
	case VerdictFail:
		if c.Preexisting || c.Undecided {
			return status.DisplayUnknown
		}
		return status.DisplayFailed
	default:
		return status.DisplayUnknown
	}
}

// progress classifies settled evidence before activity: a held failure is
// still failed even when the run is waiting on that step.
func (s Step) progress() string {
	if s.Status == StatusNotApplicable {
		return "not_applicable"
	}
	if s.Status == StatusSkipped {
		return "skipped"
	}
	r := s.CheckReading
	if r == nil && carriesCheckEvidence(s.Type) {
		r = readingOf(s.Evidence)
	}
	if isCheck(s.Type) && s.Observation {
		if s.Status == StatusError || r != nil && (r.Unreadable != "" || r.CheckErrors > 0 || len(r.Undecided) > 0) {
			return "unknown"
		}
		if s.Status == StatusFail {
			return "done"
		}
	}
	label := s.displayLabel()
	if label == status.DisplayFailed || s.OverrideBy == "" && (s.Status == StatusFail || s.Status == StatusNeedsHuman) {
		return "failed"
	}
	if s.Status == StatusPass && r != nil {
		for _, c := range r.Checks {
			if c.Verdict == VerdictFail && (c.Gates == nil || *c.Gates) {
				return "failed"
			}
		}
	}
	if label == status.DisplayUnknown || label == status.DisplayExcepted || label == status.DisplayStopped || s.Status == StatusError || s.Status == StatusPass && isCheck(s.Type) && s.evidenceAttention() {
		return "unknown"
	}
	if s.Status == StatusRunning {
		return "running"
	}
	if s.Status == StatusPending {
		return "waiting"
	}
	return "done"
}

// unassessed says whether any check in the reading could not be assessed.
func (r *checkReading) unassessed() bool {
	if r == nil {
		return false
	}
	for _, c := range r.Checks {
		if c.Verdict == VerdictCannotAssess {
			return true
		}
	}
	return false
}
