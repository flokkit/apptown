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
