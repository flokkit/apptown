package evals

import "github.com/flokkit/apptown/internal/apptown/status"

func evalLabel(state string) status.DisplayStatus {
	switch state {
	case StateRunning, StateStopping:
		return status.DisplayWorking
	case StateHeld:
		return status.DisplayWaitingForYou
	case StateFinished:
		return status.DisplayFinished
	case StateStopped:
		return status.DisplayStopped
	default:
		return status.DisplayUnknown
	}
}
func armLabel(state string) status.DisplayStatus {
	switch state {
	case ArmQueued:
		return status.DisplayWaiting
	case ArmCopied:
		return status.DisplayWaitingForYou
	case ArmCopying, ArmStarting, ArmWorking, ArmLanding:
		return status.DisplayWorking
	case ArmScored:
		return status.DisplayFinished
	case ArmFailed:
		return status.DisplayFailed
	case ArmNotRun:
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
