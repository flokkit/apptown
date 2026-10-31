package check

// The `tool` evaluator: a declared check whose evaluation is "run this named
// scanner and turn what it leaves behind into an outcome". It lets a way of
// working carry maintained tools — a framework self-check (`manage.py check
// --deploy`), a SAST scanner (bandit) — instead of an ever-growing pile of
// bespoke greps in prose.
//
// Two targets (Scanner.Target):
//
//	source     — run the tool's container against the tree (bandit, semgrep).
//	             Its report is normalized through the OSV/SARIF layer and the
//	             findings ARE the outcome: a report the tool wrote is the
//	             truth, not the exit code (a scanner may exit 0 with findings).
//	exec-image — run the tool's command INSIDE the app's built image (the
//	             framework self-check that must import settings). Exit-code
//	             contract: 0 conformant, anything else findings, a crash an
//	             error.
//
// What a tool check means beyond this is the caller's: which binding a name
// resolves to, how the app image was built, what environment it runs in, and
// whether a person has already excused what the tool reported.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/strs"
)

// ScanTimeout bounds one scanner run.
const ScanTimeout = 5 * time.Minute

// Scanners is the catalogue a tool check's name is looked up in. Resolving a
// name to an attached, configured tool is the caller's: the judge only asks.
type Scanners interface {
	Scanner(name string) (Scanner, bool)
}

// ScannersFunc makes a catalogue out of one lookup.
type ScannersFunc func(name string) (Scanner, bool)

func (f ScannersFunc) Scanner(name string) (Scanner, bool) { return f(name) }

// EvaluateTool judges one tool check: the named scanner against the source,
// or inside the image the source names.
//
// Evaluation.Findings are what the TOOL reported, itemised and normalized —
// not a roll-up of the check. A caller that excuses individual findings
// (a recorded exception, a verified false positive) applies that to them and
// decides what the check then says.
func (j *Judge) EvaluateTool(d Declared, src Source, mode Mode, namePrefix string) Evaluation {
	if j.Scanners == nil {
		return cannot(d, mode, fmt.Sprintf("the tool %q is a scanner binding this judge has no catalogue of", d.Tool))
	}
	s, ok := j.Scanners.Scanner(d.Tool)
	if !ok {
		return cannot(d, mode, fmt.Sprintf("no scanner named %q in this judge's catalogue", d.Tool))
	}
	if s.Looks() == "exec-image" {
		// It runs inside the public part's image: the check applies when
		// that part runs as a container and matches the words.
		why, err := ToolPublic(d.AppliesTo, s.AppliesTo, src)
		switch {
		case err != nil:
			return Evaluation{Declared: d, Mode: mode, Outcome: Error, Detail: err.Error()}
		case why != "":
			return notApplicable(d, mode, why)
		}
		return j.toolInImage(d, s, src, mode)
	}
	places, why, err := ToolPlaces(d.AppliesTo, s.AppliesTo, src)
	switch {
	case err != nil:
		return Evaluation{Declared: d, Mode: mode, Outcome: Error, Detail: err.Error()}
	case len(places) == 0:
		return notApplicable(d, mode, why)
	}
	var evs []Evaluation
	for _, pl := range places {
		evs = append(evs, j.toolOnSource(d, s, src, pl, mode, namePrefix))
	}
	return combine(d, mode, places, evs)
}

// PartFolderVar in a tool's command is the folder it runs in, as the
// container sees it: /work for the repository root, /work/<folder> for a
// part's.
const PartFolderVar = "${PART_FOLDER}"

// PartFolderPath is the folder's path in the container.
func PartFolderPath(folder string) string {
	if folder == "" || folder == "." {
		return "/work"
	}
	return "/work/" + strings.TrimPrefix(folder, "./")
}

// toolInImage runs the tool's command inside the app's built image. The image
// and the environment it runs with are the caller's: building one is not the
// judge's business, and without one there is nothing to assess.
func (j *Judge) toolInImage(d Declared, s Scanner, src Source, mode Mode) Evaluation {
	if j.RunImage == nil || strings.TrimSpace(src.Image) == "" {
		return cannot(d, mode, "no built app image to run this check inside")
	}
	code, output := j.RunImage(src.Image, s.Command, src.Env)
	ev := Evaluation{Declared: d, Mode: mode, Evidence: Evidence{ExitCode: &code, Image: src.Image}}
	switch {
	case code == 0:
		ev.Outcome = Conforms
	case code < 0 || looksLikeCrash(output):
		// The tool itself failed to run (an unhandled exception, an import
		// error), not the app failing a check — a crash is an ERROR, never a
		// conformance failure someone is told to "fix".
		ev.Outcome, ev.Detail = Error, ToolSummary(s.Name, output)
	default:
		ev.Outcome, ev.Detail = DoesNotConform, ToolSummary(s.Name, output)
	}
	return ev
}

// RemoveStale removes what an earlier run left at path (a report, a
// verdict) before a run that will write it: a file there afterwards is then
// this run's. Already absent is fine; a removal that fails is the error,
// since the old bytes would read as this run's answer.
func RemoveStale(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing the stale %s before the run: %w", filepath.Base(path), err)
	}
	return nil
}

// toolOnSource runs the tool's container against the tree and reads what it
// left behind.
func (j *Judge) toolOnSource(d Declared, s Scanner, src Source, pl Place, mode Mode, namePrefix string) Evaluation {
	if j.Run == nil {
		return cannot(d, mode, "no way to run a command here")
	}
	_ = os.MkdirAll(filepath.Join(src.Tree, ".apptown"), 0o755)
	if s.Report != "" {
		if err := RemoveStale(filepath.Join(src.Tree, s.Report)); err != nil {
			return Evaluation{Declared: d, Mode: mode, Outcome: Error, Detail: err.Error()}
		}
	}
	// A declared report is collected, and required, whatever its format;
	// only a format says how to read findings from it.
	structured := s.Format != "" && s.Report != ""
	argv := make([]string, len(s.Command))
	for i, a := range s.Command {
		argv[i] = strings.ReplaceAll(a, PartFolderVar, PartFolderPath(pl.Folder))
	}
	req := s.Request(src.Tree, argv)
	req.Name, req.Report = execution.UniqueName(namePrefix+"-tool"), s.Report
	req.Env = append(req.Env, placeEnv(pl, src)...)
	outcome := j.Run(req)
	ev := Evaluation{Declared: d, Mode: mode}
	if outcome.Termination == execution.TimedOut {
		ev.Outcome, ev.Detail = Error, s.Name+" timed out"
		return ev
	}
	exitCode := -1 // a run that gives no answer is an error
	if outcome.Answered() {
		exitCode = outcome.ExitCode
	}
	ev.Evidence.ExitCode = &exitCode
	if s.EmptyExit != 0 && exitCode == s.EmptyExit {
		// The tool's own word for "nothing here to judge", where the check
		// applies: the declaration and the code disagree.
		// The tool's own words when it gave any, else what the exit means.
		ev.Outcome = DoesNotConform
		ev.Detail = strs.FirstNonBlank(strs.Truncate(strings.TrimSpace(string(outcome.Output)), 300), EmptyExitDetail(s.Name, pl))
		ev.Findings = []Finding{conformanceFinding(d, ev.Detail, "")}
		return ev
	}
	if exitCode < 0 || exitCode > 1 {
		ev.Outcome = Error
		said := strs.Truncate(strings.TrimSpace(string(outcome.Output)), 200)
		if said == "" {
			said = ReportLost(s.Name)
		}
		ev.Detail = fmt.Sprintf("%s error (exit %d): %s", s.Name, exitCode, said)
		return ev
	}
	// A declared report that is missing or unreadable is an error, never a
	// clean answer; with a format, the report is the truth, not the exit code.
	if s.Report != "" && (outcome.ReportErr != nil || len(outcome.Report) == 0) {
		ev.Outcome, ev.Detail = Error, fmt.Sprintf("%s wrote no readable report: %v", s.Name, outcome.ReportErr)
		return ev
	}
	if structured {
		fs, err := Normalize(s.Format, outcome.Report, s.Name, s.Kind)
		if err != nil {
			ev.Outcome, ev.Detail = Error, fmt.Sprintf("%s's report could not be read: %v", s.Name, err)
			return ev
		}
		ev.Findings = fs
		ev.Evidence.Report = s.Report // the report was read: what it says is the answer
		if len(fs) == 0 {
			ev.Outcome = Conforms
			return ev
		}
		ev.Outcome, ev.Detail = DoesNotConform, FindingsSummary(s.Name, fs)
		return ev
	}
	if exitCode == 0 {
		ev.Outcome = Conforms
		return ev
	}
	ev.Outcome, ev.Detail = DoesNotConform, ToolSummary(s.Name, string(outcome.Output))
	return ev
}

// looksLikeCrash spots a tool that fell over (an unhandled exception, an
// import error) rather than reporting a clean finding — the difference
// between an error and a conformance failure. Django's `check` prints
// "System check identified …" for real findings and a Python traceback for a
// crash, so the traceback marker is the tell.
func looksLikeCrash(output string) bool {
	return strings.Contains(output, "Traceback (most recent call last)") ||
		strings.Contains(output, "ModuleNotFoundError") ||
		strings.Contains(output, "ImportError")
}

// ReportLost is the detail of a check that failed and whose report, what
// it printed, did not arrive: a container's stdout can be lost at its end.
// It says the report is gone, never that nothing was found.
func ReportLost(name string) string {
	return name + ": report lost (nothing it printed arrived)"
}

// ToolSummary trims a tool's raw output to a one-glance detail line. It is
// only asked of a tool that failed, so no output is a lost report.
func ToolSummary(tool, output string) string {
	out := strings.TrimSpace(output)
	if out == "" {
		return ReportLost(tool)
	}
	// Keep the first few non-empty lines — enough to name the problem.
	var lines []string
	for _, ln := range strings.Split(out, "\n") {
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, strings.TrimSpace(ln))
		}
		if len(lines) >= 4 {
			break
		}
	}
	return tool + ": " + strs.Truncate(strings.Join(lines, " · "), 280)
}

// FindingsSummary names the top findings so a reader sees WHAT, not a count.
func FindingsSummary(tool string, fs []Finding) string {
	var parts []string
	for i, f := range fs {
		if i >= 3 {
			parts = append(parts, fmt.Sprintf("+%d more", len(fs)-3))
			break
		}
		t := f.Title
		if f.Detail != "" {
			t += " — " + f.Detail
		}
		parts = append(parts, t)
	}
	return fmt.Sprintf("%s: %d finding(s) — %s", tool, len(fs), strs.Truncate(strings.Join(parts, "; "), 260))
}
