package workflow

import "github.com/flokkit/apptown/internal/apptown/status"

// Display labels the already classified activity. The reader retains Detail
// and Next separately, so a short word loses neither the reason nor an action.
func (s ActivityState) Display() status.DisplayStatus {
	switch s {
	case ActivityWorking, ActivityAgentWorking:
		return status.DisplayWorking
	case ActivityQueued:
		return status.DisplayWaiting
	case ActivityWaiting:
		return status.DisplayWaitingForYou
	case ActivityReady:
		return status.DisplayWaiting
	case ActivityBlocked:
		return status.DisplayFailed
	case ActivityAbandoned:
		return status.DisplayStopped
	case ActivityDone:
		return status.DisplayFinished
	default:
		return status.DisplayUnknown
	}
}

// Display describes a change using the owner's current snapshot. The
// same label is shown to every reader; approval eligibility is a separate fact.
func (c *Change) Display(run *RunSnapshot) status.DisplayStatus {
	if c.State == ChangeAbandoned {
		return status.DisplayStopped
	}
	if c.State == ChangeShipped {
		if c.ClosedAs != closedRecord && (c.LandedSHA != "" || c.ClosedAs == ClosedShippedWith) {
			return status.DisplayLanded
		}
		return status.DisplayFinished
	}
	if run == nil {
		if c.State == ChangeLanding {
			return status.DisplayUnknown
		}
		return status.DisplayWaiting
	}
	if run.Run.Status == RunAbandoned {
		return status.DisplayStopped
	}
	if run.Run.Status == RunDone {
		return status.DisplayWaiting
	}
	return run.State.Display()
}

// OpenDisplay describes work without a running workflow. It never assumes an approval is due.
func (s ChangeState) OpenDisplay(editing int) status.DisplayStatus {
	if editing > 0 {
		return status.DisplayWorking
	}
	if s == ChangeLanding {
		return status.DisplayWorking
	}
	return status.DisplayWaiting
}

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

// Display describes execution progress without claiming a runtime is running.
func (s RunState) Display() status.DisplayStatus {
	switch s {
	case RunRunning:
		return status.DisplayWorking
	case RunDone:
		return status.DisplayFinished
	case RunAbandoned:
		return status.DisplayStopped
	default:
		return status.DisplayUnknown
	}
}

// Display keeps failures on main and undecided findings out of the current change's failures.
func (c CheckReceipt) Display() status.DisplayStatus {
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
