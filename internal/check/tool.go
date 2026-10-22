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
	"fmt"
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
		return j.toolInImage(d, s, src, mode)
	}
	return j.toolOnSource(d, s, src, mode, namePrefix)
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

// toolOnSource runs the tool's container against the tree and reads what it
// left behind.
func (j *Judge) toolOnSource(d Declared, s Scanner, src Source, mode Mode, namePrefix string) Evaluation {
	if j.Run == nil {
		return cannot(d, mode, "no way to run a command here")
	}
	_ = os.MkdirAll(filepath.Join(src.Tree, ".apptown"), 0o755)
	if s.Report != "" {
		_ = os.Remove(filepath.Join(src.Tree, s.Report)) // never trust a stale report
	}
	network := "none"
	if s.Network {
		network = ""
	}
	// A declared report is collected, and required, whatever its format;
	// only a format says how to read findings from it.
	structured := s.Format != "" && s.Report != ""
	req := execution.Request{
		Workspace: src.Tree,
		Argv:      s.Command,
		Image:     s.Image,
		Network:   network,
		Timeout:   ScanTimeout,
		Name:      execution.UniqueName(namePrefix + "-tool"),
		Report:    s.Report,
	}
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
	if exitCode < 0 || exitCode > 1 {
		ev.Outcome = Error
		ev.Detail = fmt.Sprintf("%s error (exit %d): %s", s.Name, exitCode, strs.Truncate(strings.TrimSpace(string(outcome.Output)), 200))
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

// ToolSummary trims a tool's raw output to a one-glance detail line.
func ToolSummary(tool, output string) string {
	out := strings.TrimSpace(output)
	if out == "" {
		return tool + " reported findings"
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
