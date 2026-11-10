package evals

import "github.com/flokkit/apptown/internal/apptown/status"

func evalLabel(state string) status.DisplayStatus {
	switch state {
	case stateRunning, stateStopping:
		return status.DisplayWorking
	case stateHeld:
		return status.DisplayWaitingForYou
	case stateFinished:
		return status.DisplayFinished
	case stateStopped:
		return status.DisplayStopped
	default:
		return status.DisplayUnknown
	}
}

// withArms refines a running eval's label by its arms: while every arm is
// still queued nothing is being worked on, so the eval waits as they do
// rather than reading "in progress" above arms that say "waiting".
func withArms(label status.DisplayStatus, state string, arms []Arm) status.DisplayStatus {
	if state != stateRunning || len(arms) == 0 {
		return label
	}
	for _, a := range arms {
		if a.State != armQueued {
			return label
		}
	}
	return status.DisplayWaiting
}

func armLabel(state string) status.DisplayStatus {
	switch state {
	case armQueued:
		return status.DisplayWaiting
	case armCopied:
		return status.DisplayWaitingForYou
	case armCopying, armStarting, armWorking, armLanding:
		return status.DisplayWorking
	case armScored:
		return status.DisplayFinished
	case armFailed:
		return status.DisplayFailed
	case armNotRun:
		return status.DisplayStopped
	default:
		return status.DisplayUnknown
	}
}
func checkLabel(state string) status.DisplayStatus {
	switch state {
	case "pass":
		return status.DisplayPassed
	case "fail":
		return status.DisplayFailed
	case "pending":
		return status.DisplayWaitingForChecks
	case "running":
		return status.DisplayWorking
	case "not_applicable":
		return status.DisplayNotApplicable
	case "skipped":
		return status.DisplayStopped
	default:
		return status.DisplayUnknown
	}
}
