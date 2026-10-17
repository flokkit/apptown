package check

import (
	"strings"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/strs"
)

// The deterministic evaluator: a script in a pinned busybox-class image,
// read-only tree, no network. Exit 0 conformant, 1 findings, anything else
// the check errored (recorded, never silently dropped).

// ShImage is the pinned image every sh check runs in: alpine with git,
// because the reference for "what does this repository ignore" is git's own
// matcher (repo/no-build-artifacts asks it of the committed tree) and a tree
// question is a shell question. The image's ENTRYPOINT is git;
// runs clear it (NoEntrypoint) so `sh -c` is the command.
const ShImage = "alpine/git:v2.47.2"

// ShTimeout bounds one check's script.
const ShTimeout = 60 * time.Second

// EvaluateSh judges every sh check in the batch against the staged tree, one
// container run each.
func (j *Judge) EvaluateSh(checks []Declared, src Source, mode Mode, namePrefix string) []Evaluation {
	var out []Evaluation
	for _, d := range checks {
		if Evaluator(d.Declaration) != "sh" {
			continue
		}
		out = append(out, j.evaluateSh(d, src, mode, namePrefix))
	}
	return out
}

// evaluateSh runs one sh check against the staged tree.
func (j *Judge) evaluateSh(d Declared, src Source, mode Mode, namePrefix string) Evaluation {
	now := clock.Or(j.Now)
	start := now()
	outcome := j.Run(execution.Request{
		Workspace:    src.Tree,
		WorkspaceRO:  true,
		Argv:         []string{"sh", "-c", d.Sh},
		Image:        ShImage,
		NoEntrypoint: true,
		Network:      "none",
		Timeout:      ShTimeout,
		Name:         execution.UniqueName(namePrefix),
	})
	exitCode := -1 // a run that gives no answer is an error
	if outcome.Answered() {
		exitCode = outcome.ExitCode
	}
	msg := strings.TrimSpace(string(outcome.Output))
	ev := Evaluation{Declared: d, Mode: mode,
		Evidence: Evidence{ExitCode: &exitCode, Duration: now().Sub(start)}}
	switch {
	case exitCode == 0:
		ev.Outcome = Conforms
	case exitCode == 1:
		ev.Outcome = DoesNotConform
		if msg != "" {
			ev.Evidence.Output = strs.Truncate(msg, 600) // the check's own words are the evidence
		}
		f := conformanceFinding(d, msg, "")
		if d.Occurrences == "lines" {
			f.Items = OccurrenceLines(msg)
			ev.Occurrences = f.Items
		}
		ev.Findings = []Finding{f}
	default:
		ev.Outcome = Error
		ev.Evidence.Output = strs.Truncate(msg, 300)
	}
	return ev
}

// OccurrenceLines splits a check's output into its occurrences: one per
// non-blank line, trimmed, duplicates kept (two identical lines are two
// occurrences — the baseline matches them one for one).
func OccurrenceLines(out string) []string {
	var items []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			items = append(items, l)
		}
	}
	return items
}
