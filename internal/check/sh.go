package check

import (
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/strs"
)

// The deterministic evaluator: a script in a pinned busybox-class image,
// read-only tree, no network. Exit 0 conformant, 1 findings, anything else
// the check errored (recorded, never silently dropped).

// Every sh check runs in Judge.ShImage, alpine with git (the build's
// tool versions name it by digest), because the reference for "what does
// this repository ignore" is git's own matcher (repo/no-build-artifacts asks
// it of the committed tree) and a tree question is a shell question. The
// image's ENTRYPOINT is git; runs clear it (NoEntrypoint) so `sh -c` is the
// command.

// shTimeout bounds one check's script.
const shTimeout = 60 * time.Second

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

// evaluateSh runs one sh check against the staged tree: once in each
// folder of the parts it applies to, or not at all when it applies to none.
func (j *Judge) evaluateSh(d Declared, src Source, mode Mode, namePrefix string) Evaluation {
	places, why, err := Applies(d.AppliesTo, src)
	switch {
	case err != nil:
		return Evaluation{Declared: d, Mode: mode, Outcome: Error, Detail: err.Error()}
	case len(places) == 0:
		return notApplicable(d, mode, why)
	}
	image := j.ShImage
	if image == "" {
		// No shell image is no verdict: recorded as the check's error.
		exitCode := -1
		return Evaluation{Declared: d, Mode: mode, Outcome: Error, Evidence: evidence{ExitCode: &exitCode, Output: "no shell image: the tool versions name it"}}
	}
	var evs []Evaluation
	for _, pl := range places {
		evs = append(evs, j.shAt(d, src, pl, mode, image, namePrefix))
	}
	return combine(d, mode, places, evs)
}

// shPrelude runs before every sh check's script: the repository root is
// kept in APPTOWN_ROOT, then the script runs in its folder. A declared
// folder the tree does not have is the check's finding: the contract names
// what is not there.
const shPrelude = `APPTOWN_ROOT=$PWD; export APPTOWN_ROOT; cd -- "$APPTOWN_FOLDER" 2>/dev/null || { echo "the contract names the folder $APPTOWN_FOLDER, which is not in the tree"; exit 1; }
`

// shAt runs one sh check in one place.
func (j *Judge) shAt(d Declared, src Source, pl Place, mode Mode, image, namePrefix string) Evaluation {
	now := clock.Or(j.Now)
	start := now()
	outcome := j.Run(execution.Request{
		Workspace:    src.Tree,
		WorkspaceRO:  true,
		Argv:         []string{"sh", "-c", shPrelude + d.Sh},
		Env:          placeEnv(pl, src),
		Image:        image,
		NoEntrypoint: true,
		Network:      "none",
		Timeout:      shTimeout,
		Name:         execution.UniqueName(namePrefix),
	})
	exitCode := -1 // a run that gives no answer is an error
	if outcome.Answered() {
		exitCode = outcome.ExitCode
	}
	msg := strings.TrimSpace(string(outcome.Output))
	ev := Evaluation{Declared: d, Mode: mode,
		Evidence: evidence{ExitCode: &exitCode, Duration: now().Sub(start)}}
	switch {
	case exitCode == 0:
		ev.Outcome = Conforms
	case exitCode == 1:
		ev.Outcome = DoesNotConform
		if msg != "" {
			ev.Evidence.Output = strs.Truncate(msg, 600) // the check's own words are the evidence
		} else {
			// The exit code is the verdict; its words did not arrive, which
			// is never "no occurrences" (a lines check's are then undecided).
			ev.Evidence.Output = ReportLost(d.Name)
		}
		f := conformanceFinding(d, msg, "")
		if d.Occurrences == "lines" {
			f.Items = occurrenceLines(msg)
			ev.Occurrences = f.Items
		}
		ev.Findings = []Finding{f}
	default:
		ev.Outcome = Error
		ev.Evidence.Output = strs.Truncate(strs.FirstNonBlank(msg, ReportLost(d.Name)), 300)
	}
	return ev
}

// occurrenceLines splits a check's output into its occurrences: one per
// non-blank line, trimmed, duplicates kept (two identical lines are two
// occurrences — the baseline matches them one for one).
func occurrenceLines(out string) []string {
	var items []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			items = append(items, l)
		}
	}
	return items
}
