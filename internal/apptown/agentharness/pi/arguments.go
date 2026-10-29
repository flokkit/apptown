package pi

import "path/filepath"

// StepPaths are where a step's Pi keeps its transcripts and finds its
// steering, as the container sees them. SessionFile, when set, is the one
// transcript file Pi is to use (--session): Pi then looks nothing up in
// Sessions by id, so no other file there naming the session can be the one
// it resumes.
type StepPaths struct{ Sessions, Steering, SessionFile string }

// FolderStepPaths: Pi on AppTown's host works in the run's folder, with
// its transcripts and steering under .apptown.
var FolderStepPaths = StepPaths{Sessions: filepath.Join(".apptown", "sessions"), Steering: filepath.Join(".apptown", "steering.md")}

func StepArgs(at StepPaths, sessionID string, steered bool, model, thinking, prompt string) []string {
	args := []string{"--print", "--mode", "json", "--no-context-files"}
	if at.SessionFile != "" {
		args = append(args, "--session", at.SessionFile)
	} else {
		args = append(args, "--session-dir", at.Sessions, "--session-id", sessionID)
	}
	if steered {
		args = append(args, "--append-system-prompt", at.Steering)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if thinking != "" {
		args = append(args, "--thinking", thinking)
	}
	return append(args, prompt)
}

// ReviewArgs is a review's argv on AppTown's own machine: its sessions
// under .apptown/sessions, a wrap-up or fixup continuing the newest one.
func ReviewArgs(model, thinking string, resume bool, message string) []string {
	session := []string{"--session-dir", filepath.Join(".apptown", "sessions")}
	if resume {
		session = append(session, "--continue")
	}
	return reviewArgs(session, model, thinking, message)
}

// SessionReviewArgs is a review's argv when its transcript file is named:
// every invocation, the review and its wrap-up or fixup, points Pi at that
// exact file (--session), so a wrap-up resumes the review's own session and
// never "the newest", nor another file naming the same session.
func SessionReviewArgs(sessionFile, model, thinking, message string) []string {
	return reviewArgs([]string{"--session", sessionFile}, model, thinking, message)
}

func reviewArgs(session []string, model, thinking, message string) []string {
	args := append([]string{"--print", "--mode", "json",
		"--no-context-files", "--no-skills", "--no-extensions"}, session...)
	if model != "" {
		args = append(args, "--model", model)
	}
	if thinking != "" {
		args = append(args, "--thinking", thinking)
	}
	return append(args, message)
}
