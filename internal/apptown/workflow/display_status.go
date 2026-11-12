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
