package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

// waitVerdict reads AppTown's own activity for a running run (the state
// the change page shows) and reports the moment waiting stops being useful:
// "decision" (a person must approve), "wedged" (the stage is blocked and
// cannot pass on its own), "reworking" (AppTown says the stage's agent is
// working from send-back words, a person's or a check's), or "working". AppTown, not this client, decides
// what is settled: a verdict about to be checked again after a rework and a
// landing still taking its acceptance read as working there, so wait no
// longer calls them wedged or a person's move in the seconds before they
// settle.
func waitVerdict(d runDetail) (kind string, stepID ids.ID, name string) {
	var approval, failed ids.ID
	var aName, fName string
	for _, s := range d.Steps {
		if s.Stage != d.Run.CurrentStage {
			continue
		}
		if s.Type == "approval" && s.Status == "pending" && approval.IsZero() {
			approval, aName = s.ID, s.Name
		}
		if (s.Status == "fail" || s.Status == "error" || s.Status == "needs_human") && s.OverrideBy == "" && failed.IsZero() {
			failed, fName = s.ID, s.Name
		}
	}
	switch d.State {
	case "waiting on you":
		if approval.IsZero() {
			// The approval is in a called workflow; AppTown names it.
			return "decision", ids.Nil, d.StateDetail
		}
		return "decision", approval, aName
	case "blocked":
		if failed.IsZero() {
			return "wedged", ids.Nil, d.StateDetail
		}
		return "wedged", failed, fName
	}
	// A rework is announced so no human plays redundant transport: the
	// server names the step and who sent it back.
	if sb := d.SentBack; sb != nil {
		return "reworking", sb.StepID, fmt.Sprintf("%s, sent back by %s", sb.Step, sb.By)
	}
	return "working", ids.Nil, ""
}

// approveHint is the decision line: the approval and how to give it.
func approveHint(name string, id ids.ID) string {
	if id.IsZero() {
		return name
	}
	return fmt.Sprintf("%s — apptown approve %d (or reject)", name, id)
}

// stepRef names a step for a sentence, with its id when AppTown gave one.
func stepRef(name string, id ids.ID) string {
	if id.IsZero() {
		return name
	}
	return fmt.Sprintf("%s (%d)", name, id)
}

// cmdWait blocks until there is nothing to wait FOR: the run or change
// finishes, fails, or stops at a decision a person must make. It prints
// what happened and, when a decision is needed, exactly which step.
// Exit codes: 0 finished well · 1 abandoned/blocked · 2 needs a person.
// exitProcess ends the CLI with a code; wait's tests replace it to read the
// code instead.
var exitProcess = os.Exit

func cmdWait(args []string) error {
	fs := newFlags("wait")
	change := fs.Bool("c", false, "the id is a change's, not a run's (<project>#<number> always names a change)")
	release := fs.Bool("r", false, "the id is a release number of the project named by -p")
	project := fs.String("p", "", "the project a bare number names a change or a release (-r) in")
	operation := fs.Bool("o", false, "the id is an operation's (a copy, a check, a take-down)")
	interval := fs.Int("i", 10, "poll interval in seconds")
	asJSON := fs.Bool("json", false, "print how the wait ended as JSON (exit codes unchanged)")
	args = parseArgs(fs, args)
	if len(args) >= 1 && args[0] == "operation" {
		*operation, args = true, args[1:]
	}
	if len(args) < 1 {
		return usagef("usage: apptown wait [-c] <change|run-id> [-i seconds] (a change's id or <project>#<number>, or a run's id), apptown wait -p <slug> -r <release-number>, or apptown wait operation <id>")
	}
	if *operation {
		id, err := parseID(args[0], "operation id")
		if err != nil {
			return err
		}
		if !*asJSON {
			return followOperation(os.Stdout, id, time.Duration(*interval)*time.Second)
		}
		// Only an observed ending is an outcome: a read that failed is an
		// error with no document, as on a run or a change.
		op, err := watchOperation(os.Stderr, id, time.Duration(*interval)*time.Second)
		if err != nil {
			return err
		}
		out := WaitOutcome{Target: Target{Kind: "operation", ID: id}, Outcome: "done", Detail: op.Detail}
		switch op.State {
		case "succeeded":
		case "unknown":
			out.Outcome, out.ExitCode = "unknown", exitUnknown
		default:
			out.Outcome, out.ExitCode = "failed", 1
		}
		if err := printJSON(out); err != nil {
			return err
		}
		return operationEnding(op)
	}
	if *release {
		if *asJSON {
			return usagef("--json covers waiting on a run, a change or an operation, not a release")
		}
		return waitRelease(*project, args[0], time.Duration(*interval)*time.Second)
	}
	// A change's name (<project>#<number>, or a bare number in a project)
	// is what a script holds: wait takes that form as the change, never as
	// the run with the same number. A run is named by its id.
	if id, err := ids.Parse(strings.TrimSpace(args[0])); err != nil || id.IsZero() {
		*change = true
	}
	resolve := func(args []string) error { return runArg(args, *project) }
	if *change {
		resolve = func(args []string) error { return changeArg(args, *project) }
	}
	if err := resolve(args); err != nil {
		return err
	}
	id := args[0]
	progress := io.Writer(os.Stdout)
	if *asJSON {
		progress = os.Stderr
	}
	lines := pollLines{out: progress}
	note := lines.note
	// stop ends the wait: the outcome as JSON, or the line for a person,
	// then the exit code, the same either way. run is the run the outcome
	// is about, step the approval or failed step, when there is one.
	stop := func(o WaitOutcome, run *runDetail, step ids.ID, line string) error {
		if *asJSON {
			o.Target = Target{Kind: "run"}
			if *change {
				o.Target.Kind = "change"
			}
			o.Target.ID, _ = ids.Parse(id)
			if run != nil && !run.Run.ID.IsZero() {
				o.Run = &RunRef{ID: run.Run.ID, Number: run.Run.Number}
			}
			if !step.IsZero() {
				o.Step = &step
			}
			if err := printJSON(o); err != nil {
				return err
			}
		} else {
			fmt.Println(line)
		}
		if o.ExitCode != 0 {
			exitProcess(o.ExitCode)
		}
		return nil
	}
	// A poll that fails is not an answer: AppTown restarts for a deploy
	// and answers 502 for a few seconds. Ride out a run of failures before
	// giving up, so a wait armed before a deploy is still there after it.
	// An answer that will not change (no such run) is given at once.
	misses := 0
	poll := func(path string, out any) (bool, error) {
		if err := api.get(path, out); err != nil {
			var refused *httpError
			if errors.As(err, &refused) && refused.final() {
				// A run id that names no run is often a change's: wait on
				// the change, as the person meant.
				if refused.Status == 404 && !*change && isChange(id) {
					fmt.Fprintf(progress, "… %s is a change, not a run: waiting on the change\n", id)
					*change = true
					return false, nil
				}
				return false, err
			}
			misses++
			if misses >= 10 {
				return false, err
			}
			fmt.Fprintf(os.Stderr, "… %v (retrying)\n", err)
			return false, nil
		}
		misses = 0
		lines.next()
		return true, nil
	}
	for {
		if *change {
			var v struct {
				Change       struct{ State string }
				State        string
				StateDetail  string
				Detail       *runDetail
				MainConflict *mainConflict
			}
			if ok, err := poll("/changes/"+id, &v); err != nil {
				return err
			} else if !ok {
				pause(time.Duration(*interval) * time.Second)
				continue
			}
			if v.MainConflict != nil {
				note(v.MainConflict.line(id))
			}
			switch v.Change.State {
			case "shipped":
				return stop(WaitOutcome{Outcome: "shipped", Detail: "shipped"}, v.Detail, ids.Nil, fmt.Sprintf("✓ change %s shipped", id))
			case "abandoned":
				return stop(WaitOutcome{Outcome: "abandoned", ExitCode: 1, Detail: "abandoned"}, v.Detail, ids.Nil, fmt.Sprintf("✗ change %s abandoned", id))
			}
			switch {
			case v.Detail != nil && v.Detail.Run.Status == "running":
				d := *v.Detail
				note(fmt.Sprintf("… change %s: run %d (%s) in %s", id, d.Run.ID, d.Run.WorkflowName, d.Run.CurrentStage))
				switch kind, sid, name := waitVerdict(d); kind {
				case "decision":
					return stop(WaitOutcome{Outcome: "needs_decision", ExitCode: 2, Detail: name}, &d, sid, fmt.Sprintf("⏸ change %s needs a decision: %s", id, approveHint(name, sid)))
				case "wedged":
					return stop(WaitOutcome{Outcome: "blocked", ExitCode: 1, Detail: name}, &d, sid, fmt.Sprintf("✗ change %s: run %d is blocked — %s failed; fix, send back, override, or abandon", id, d.Run.ID, stepRef(name, sid)))
				case "reworking":
					note(fmt.Sprintf("↻ change %s: %s — the agent is reworking it (no action needed)", id, name))
				}
			case v.State == "queued":
				// Accepted work whose landing has not started yet.
				note(fmt.Sprintf("… change %s: %s queued", id, v.StateDetail))
			default:
				return stop(WaitOutcome{Outcome: "ended", ExitCode: 2, Detail: endedDetail(v.Change.State, v.Detail)}, v.Detail, ids.Nil, noRunGoing(id, v.Change.State, v.Detail))
			}
		} else {
			var d runDetail
			if ok, err := poll("/runs/"+id, &d); err != nil {
				return err
			} else if !ok {
				pause(time.Duration(*interval) * time.Second)
				continue
			}
			switch d.Run.Status {
			case "done":
				return stop(WaitOutcome{Outcome: "done", Detail: "done"}, &d, ids.Nil, fmt.Sprintf("✓ run %s done (%s)", id, d.Run.WorkflowName))
			case "running":
				note(fmt.Sprintf("… run %s (%s) in %s", id, d.Run.WorkflowName, d.Run.CurrentStage))
				switch kind, sid, name := waitVerdict(d); kind {
				case "decision":
					return stop(WaitOutcome{Outcome: "needs_decision", ExitCode: 2, Detail: name}, &d, sid, fmt.Sprintf("⏸ run %s needs a decision: %s", id, approveHint(name, sid)))
				case "wedged":
					return stop(WaitOutcome{Outcome: "blocked", ExitCode: 1, Detail: name}, &d, sid, fmt.Sprintf("✗ run %s is blocked — %s failed; fix, send back, override, or abandon", id, stepRef(name, sid)))
				case "reworking":
					note(fmt.Sprintf("↻ run %s: %s — the agent is reworking it (no action needed)", id, name))
				}
			default:
				return stop(WaitOutcome{Outcome: "failed", ExitCode: 1, Detail: endedDetail(d.Run.Status, &d)}, &d, ids.Nil, runEnded(id, d))
			}
		}
		pause(time.Duration(*interval) * time.Second)
	}
}

// endedDetail is wait --json's sentence for a run or change that stopped
// with no run going: AppTown's ending when it gave one, else the state.
func endedDetail(state string, last *runDetail) string {
	if last != nil && last.Ending != "" {
		return last.Ending
	}
	return state
}

// waitRelease follows a release's log until it settles: exit 0 when it is
// released, 1 when it failed. A release is numbered within its project, so
// the project is required.
func waitRelease(project, arg string, interval time.Duration) error {
	if project == "" {
		return usagef("usage: apptown wait -p <slug> -r <release-number>: a release is numbered within its project")
	}
	number, err := parseNumber(strings.TrimPrefix(arg, "r"), "release number")
	if err != nil {
		return err
	}
	state, err := followRelease(project, number, interval)
	if err != nil {
		return err
	}
	if state == "released" {
		fmt.Printf("✓ release r%d of %s released\n", number, project)
		return nil
	}
	return fmt.Errorf("release r%d of %s failed — the log above shows where it stopped", number, project)
}

// mainConflict is main AppTown could not merge into a change before a
// step, while the change's branch does not contain it yet.
type mainConflict struct {
	MainBranch, Main, Step, CatchUp string
	Files                           []string
}

// line says the conflict and the catch-up, in AppTown's words.
func (c mainConflict) line(id string) string {
	return fmt.Sprintf("! change %s: %s has moved and AppTown could not merge it in before step %s: %s conflict. Catch up in the change's chat: %s",
		id, c.MainBranch, c.Step, strings.Join(c.Files, ", "), c.CatchUp)
}

// noRunGoing is wait's answer for a change with no run going. When its last
// run ended without finishing (a landing that found main had moved, a
// person's stop), it says why in AppTown's words, which carry what to do
// next: a chat told only "a person's move" went on saying its landing
// awaited approval (change 61).
func noRunGoing(id, state string, last *runDetail) string {
	if last != nil && last.Ending != "" {
		return fmt.Sprintf("⏸ change %s is %s with no run going — run %d (%s) ended: %s", id, state, last.Run.ID, last.Run.WorkflowName, last.Ending)
	}
	return fmt.Sprintf("⏸ change %s is %s with no run going — a person's move", id, state)
}

// runEnded is wait's answer for a run that ended without finishing, with
// why when AppTown says.
func runEnded(id string, d runDetail) string {
	if d.Ending != "" {
		return fmt.Sprintf("✗ run %s %s: %s", id, d.Run.Status, d.Ending)
	}
	return fmt.Sprintf("✗ run %s %s", id, d.Run.Status)
}

// pollLines prints what a poll says that the poll before it did not. A
// state can take more than one line (a run in its stage, and the rework in
// it); compared only with the last line printed, the two printed in turn on
// every poll.
type pollLines struct {
	out       io.Writer
	prev, cur map[string]bool
}

// next begins a poll's lines.
func (p *pollLines) next() { p.prev, p.cur = p.cur, map[string]bool{} }

func (p *pollLines) note(s string) {
	if p.cur == nil {
		p.cur = map[string]bool{}
	}
	if !p.prev[s] && !p.cur[s] {
		fmt.Fprintln(p.out, s)
	}
	p.cur[s] = true
}

// isChange says whether id names a change.
func isChange(id string) bool {
	var ch struct{ Change struct{ ID ids.ID } }
	return api.get("/changes/"+id, &ch) == nil && !ch.Change.ID.IsZero()
}

// cmdFindings lists a run's scan/review findings — what a failed scan gate
// actually found, without leaving the terminal.
func cmdFindings(args []string) error {
	if len(args) < 1 {
		return usagef("usage: apptown findings <run-id>")
	}
	var out struct {
		Findings []struct {
			Kind, Severity, Title, Source string
			Identifiers                   []string
			Package                       struct{ Name, Version, FixedIn string }
		}
	}
	if err := runArg(args, ""); err != nil {
		return err
	}
	if err := api.get("/runs/"+args[0]+"/findings", &out); err != nil {
		return err
	}
	w := tw()
	fmt.Fprintln(w, "SEV\tKIND\tPACKAGE\tTITLE\tFIX")
	for _, f := range out.Findings {
		id := ""
		if len(f.Identifiers) > 0 {
			id = " (" + f.Identifiers[0] + ")"
		}
		fix := f.Package.FixedIn
		if fix != "" {
			fix = "→ " + fix
		}
		fmt.Fprintf(w, "%s\t%s\t%s@%s\t%s%s\t%s\n", f.Severity, f.Kind,
			f.Package.Name, f.Package.Version, f.Title, id, fix)
	}
	return w.Flush()
}

func cmdRuns(args []string) error {
	fs := newFlags("runs")
	project := fs.String("p", "", "project slug")
	status := fs.String("status", "running", "running | done | failed | all")
	parseArgs(fs, args)
	q := "?status=" + *status
	if *project != "" {
		q += "&project=" + *project
	}
	var runs []struct {
		ID           ids.ID
		Status       string
		CurrentStage string
		WorkflowName string
		ItemTitle    string
		ProjectSlug  string
		State        string
		StateDetail  string
	}
	if err := api.get("/runs"+q, &runs); err != nil {
		return err
	}
	w := tw()
	fmt.Fprintln(w, "RUN\tPROJECT\tTITLE\tWORKFLOW\tSTAGE\tSTATUS")
	for _, r := range runs {
		status := r.Status
		// "running" is a lie for a run wedged on failed gates or waiting on a
		// person — show the honest state a human (or helper) must act on.
		if r.State != "" && r.State != "working" && r.State != "agent working" {
			status = r.Status + " — " + r.State
			if r.StateDetail != "" {
				status += ": " + r.StateDetail
			}
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.ProjectSlug, r.ItemTitle, r.WorkflowName, r.CurrentStage, status)
	}
	return w.Flush()
}

type runDetail struct {
	Run struct {
		ID           ids.ID
		Number       int64
		Status       string
		CurrentStage string
		WorkflowName string
	}
	State       string
	StateDetail string
	// SentBack is the rework AppTown says the current stage is in.
	SentBack *struct {
		StepID ids.ID
		Step   string
		By     string
		Note   string
	}
	// Ending is why an abandoned run ended, with what to do next.
	Ending string
	Stages []string
	Steps  []struct {
		ID             ids.ID
		Stage          string
		Name           string
		Type           string
		Status         string
		OverrideBy     string
		OverrideReason string
		Evidence       map[string]any
	}
}

func cmdStatus(args []string) error {
	if len(args) < 1 {
		return usagef("usage: apptown status <run-id>")
	}
	if err := runArg(args, ""); err != nil {
		return err
	}
	var d runDetail
	if err := api.get("/runs/"+args[0], &d); err != nil {
		return err
	}
	marks := make([]string, 0, len(d.Stages)+1)
	passed := true
	for _, s := range d.Stages {
		if s == d.Run.CurrentStage && d.Run.Status == "running" {
			marks = append(marks, "["+s+"]")
			passed = false
		} else if passed || d.Run.Status == "done" {
			marks = append(marks, s+" ✓")
		} else {
			marks = append(marks, s)
		}
	}
	status := d.Run.Status
	if d.State != "" && d.State != "working" && d.State != "agent working" {
		status += " — " + d.State
		if d.StateDetail != "" {
			status += ": " + d.StateDetail
		}
	}
	fmt.Printf("run %d · %s · %s\n", d.Run.ID, d.Run.WorkflowName, status)
	fmt.Printf("stages: %s\n\n", strings.Join(marks, " → "))
	w := tw()
	fmt.Fprintln(w, "STEP\tSTAGE\tNAME\tTYPE\tSTATUS")
	for _, s := range d.Steps {
		status := s.Status
		if s.OverrideBy != "" {
			status = "pass (overridden by " + s.OverrideBy + ")"
		} else if note := testsNote(s.Type, s.Status, s.Evidence); note != "" {
			status += " (" + note + ")"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", s.ID, s.Stage, s.Name, s.Type, status)
	}
	w.Flush()
	if d.Run.Status == "running" {
		fmt.Printf("\nnext: apptown advance %d · approve/reject <step-id> · run-step <step-id> · attach %d -k <kind>\n",
			d.Run.ID, d.Run.ID)
	}
	return nil
}

// testsNote is what a finished tests step's report said, when that is not
// a plain pass: its skips, or that its counts are not known (no report, an
// unreadable one, a receipt from before reports, or no tests found). Empty
// for counts with no skips, and for any other step.
func testsNote(kind, status string, ev map[string]any) string {
	if kind != "tests" || (status != "pass" && status != "fail") {
		return ""
	}
	if c, ok := ev["testCounts"].(map[string]any); ok {
		n := func(k string) int { f, _ := c[k].(float64); return int(f) }
		if n("skipped") == 0 {
			return ""
		}
		return fmt.Sprintf("%d of %d skipped", n("skipped"), n("passed")+n("failed")+n("skipped"))
	}
	if why, ok := ev["testReportUnreadable"].(string); ok && why != "" {
		return "test report unreadable: " + why
	}
	if cmd, _ := ev["command"].(string); cmd == "" {
		return "no tests found"
	}
	return "test counts not reported"
}

// testsLines are apptown show's Tests lines: one per tests step of the runs
// whose reading is not a plain pass, naming the first skipped tests.
func testsLines(runs []runDetail) []string {
	var lines []string
	for _, d := range runs {
		for _, s := range d.Steps {
			note := testsNote(s.Type, s.Status, s.Evidence)
			if note == "" || s.OverrideBy != "" {
				continue
			}
			line := fmt.Sprintf("run %d %s: %s", d.Run.ID, s.Name, note)
			if names, ok := s.Evidence["skippedTests"].([]any); ok && len(names) > 0 {
				shown := make([]string, 0, 3)
				for _, n := range names[:min(3, len(names))] {
					shown = append(shown, fmt.Sprint(n))
				}
				line += " — " + strings.Join(shown, ", ")
				if len(names) > 3 {
					line += ", …"
				}
			}
			lines = append(lines, line)
		}
	}
	return lines
}

func cmdAdvance(args []string) error {
	if len(args) < 1 {
		return usagef("usage: apptown advance <run-id>")
	}
	if err := runArg(args, ""); err != nil {
		return err
	}
	raw, err := api.request("POST", "/runs/"+args[0]+"/advance", nil)
	if err != nil {
		return err
	}
	var res struct {
		Advanced  bool
		RunStatus string
		Stage     string
		Blocking  []struct {
			ID     ids.ID
			Name   string
			Status string
		}
		Missing []string
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return err
	}
	switch {
	case res.Advanced && res.RunStatus == "done":
		fmt.Println("advanced — run is done ✓")
	case res.Advanced:
		fmt.Printf("advanced to stage %q\n", res.Stage)
	default:
		fmt.Printf("blocked at stage %q by:\n", res.Stage)
		for _, b := range res.Blocking {
			fmt.Printf("  step %d %s (%s)\n", b.ID, b.Name, b.Status)
		}
		// Missing evidence blocks with every step passed, so it must be
		// listed or nothing would show under "by:".
		for _, m := range res.Missing {
			fmt.Printf("  missing evidence: %s\n", m)
		}
	}
	return nil
}

// approvalQuestion is what approving a step agrees to (GET
// /steps/{id}/question).
type approvalQuestion struct {
	Step, Status, Means, Instructions string
	LandingMeans                      []string
	// Adds is what the approval's commit brings into the app; nil when
	// the approval assents to no commit.
	Adds *changeAdds
}

// changeAdds is AppTown's list of what a change adds, worded by
// AppTown; the client only lays it out.
type changeAdds struct {
	Digest, ReadFrom, NothingNew string
	Lines                        []struct{ Kind, Text string }
}

// printAdds prints the "This change adds" section. Its note names what
// was read, so "nothing new" never claims more than AppTown looked at.
func printAdds(w io.Writer, a *changeAdds, indent string) {
	if a == nil {
		return
	}
	note := "Approving accepts these into the app."
	if a.ReadFrom != "" {
		note += " Read from " + a.ReadFrom + "."
	}
	fmt.Fprintf(w, "%s%s\n%s%s\n", indent, bold("This change adds"), indent, dim(note))
	if a.NothingNew != "" {
		fmt.Fprintf(w, "%s  %s\n", indent, a.NothingNew)
	}
	for _, l := range a.Lines {
		switch l.Kind {
		case "added":
			fmt.Fprintf(w, "%s  %s %s\n", indent, green("+"), l.Text)
		case "removed":
			fmt.Fprintf(w, "%s  %s %s\n", indent, red("−"), l.Text)
		default:
			fmt.Fprintf(w, "%s  %s %s\n", indent, yellow("?"), l.Text)
		}
	}
}

// addsDigest is what an approval sends back of the list it showed: the
// AppTown refuses an approval of a commit that does not send it.
func (q approvalQuestion) addsDigest() string {
	if q.Adds == nil {
		return ""
	}
	return q.Adds.Digest
}

// showQuestion prints what approving a step agrees to: its own sentence, and
// for a change's acceptance the landing's, for which it may also count. An
// approval's sentence is shown before the decision is sent, in every client.
func showQuestion(w io.Writer, stepID string) (approvalQuestion, error) {
	var q approvalQuestion
	if err := api.get("/steps/"+stepID+"/question", &q); err != nil {
		return q, err
	}
	printQuestion(w, q)
	return q, nil
}

// printQuestion prints what approving agrees to, in full.
func printQuestion(w io.Writer, q approvalQuestion) {
	if q.Means != "" {
		fmt.Fprintf(w, "  Approving means: “%s”\n", q.Means)
	}
	for _, m := range q.LandingMeans {
		fmt.Fprintf(w, "  If you may approve the landing, this also approves it: “%s”\n", m)
	}
	if q.Instructions != "" {
		fmt.Fprintf(w, "  %s %s\n", yellow("●"), q.Instructions)
	}
	printAdds(w, q.Adds, "  ")
}

// questionLine is what approving agrees to in one line, for -y, where the
// person has already said yes: the sentence, and the landing's beside it.
func questionLine(name string, q approvalQuestion) string {
	line := fmt.Sprintf("  → approving %s (-y): “%s”", name, q.Means)
	for _, m := range q.LandingMeans {
		line += dim(fmt.Sprintf(" — and the landing: “%s”", m))
	}
	return line
}

func cmdDecide(args []string, decision string) error {
	fs := newFlags(decision)
	comment := fs.String("m", "", "comment")
	yes := fs.Bool("y", false, "approve without asking; you have read what it means")
	asJSON := fs.Bool("json", false, "print the decision as JSON (needs -y: --json never asks)")
	args = parseArgs(fs, args)
	if len(args) < 1 {
		return usagef("usage: apptown %s <step-id> [-m comment]", decision)
	}
	if *asJSON && decision == "approve" && !*yes {
		return usagef("--json never asks: read what approving means (apptown approve %s), then rerun with -y --json", args[0])
	}
	if err := idArg(args, "step id"); err != nil {
		return err
	}
	shownTo := io.Writer(os.Stdout)
	if *asJSON {
		shownTo = os.Stderr
	}
	var shown []string
	var adds string
	if decision == "approve" {
		q, err := showQuestion(shownTo, args[0])
		if err != nil {
			return err
		}
		shown, adds = q.LandingMeans, q.addsDigest()
		if !*yes {
			if !isTTY {
				return usagef("read what approving %q means above, then rerun with -y (stdin is not a terminal)", q.Step)
			}
			if ans := strings.ToLower(ask("  approve? [y/N]: ")); ans != "y" && ans != "yes" {
				fmt.Println("left pending")
				return nil
			}
		}
	}
	body := map[string]any{"user": api.actor, "decision": decision, "comment": *comment}
	if shown != nil {
		body["alsoApproves"] = shown
	}
	if adds != "" {
		body["adds"] = adds
	}
	raw, err := api.request("POST", "/steps/"+args[0]+"/approve", body)
	if err != nil {
		return err
	}
	var s struct{ Name, Status string }
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("the %s was recorded, but AppTown's answer could not be read: %w", decision, err)
	}
	if *asJSON {
		step, _ := ids.Parse(args[0])
		return printJSON(Decided{Step: step, Decision: decision, Status: s.Status})
	}
	fmt.Printf("%s: %s → %s\n", decision, s.Name, s.Status)
	return nil
}

func cmdRunStep(args []string) error {
	if len(args) < 1 {
		return usagef("usage: apptown run-step <step-id>")
	}
	if err := idArg(args, "step id"); err != nil {
		return err
	}
	raw, err := api.request("POST", "/steps/"+args[0]+"/run", nil)
	if err != nil {
		return err
	}
	var s struct{ Name, Status string }
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("the step was started, but AppTown's answer could not be read: %w", err)
	}
	if s.Name != "" {
		fmt.Printf("%s → %s\n", s.Name, s.Status)
	} else {
		fmt.Println("running (check apptown status)")
	}
	return nil
}

func cmdAttach(args []string) error {
	fs := newFlags("attach")
	kind := fs.String("k", "", "artifact kind (required)")
	name := fs.String("n", "", "artifact name")
	ref := fs.String("r", "", "url or reference")
	args = parseArgs(fs, args)
	if len(args) < 1 {
		return usagef("usage: apptown attach <run-id> -k <kind> [-n name] [-r ref]")
	}
	if err := runArg(args, ""); err != nil {
		return err
	}
	if *kind == "" {
		return usagef("-k <kind> is required")
	}
	_, err := api.request("POST", "/runs/"+args[0]+"/artifacts",
		map[string]string{"kind": *kind, "name": *name, "ref": *ref})
	if err != nil {
		return err
	}
	fmt.Printf("artifact %q attached to run %s\n", *kind, args[0])
	return nil
}

func cmdTrace(args []string) error {
	fs := newFlags("trace")
	asJSON := fs.Bool("json", false, "raw JSON (for agents)")
	args = parseArgs(fs, args)
	if len(args) < 1 {
		return usagef("usage: apptown trace <run-id> [--json]")
	}
	if err := runArg(args, ""); err != nil {
		return err
	}
	raw, err := api.request("GET", "/runs/"+args[0]+"/trace", nil)
	if err != nil {
		return err
	}
	if *asJSON {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, raw, "", "  "); err != nil {
			return err
		}
		fmt.Println(pretty.String())
		return nil
	}
	var t struct {
		Run struct {
			ID           ids.ID
			Status       string
			WorkflowName string
		}
		ProjectSlug string
		Change      *struct {
			ID     ids.ID
			Number int64
			Title  string
		}
		Events []struct {
			At      string
			Kind    string
			Payload map[string]any
		}
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return err
	}
	fmt.Printf("trace of run %s · %s/%s · %s", t.Run.ID, t.ProjectSlug, t.Run.WorkflowName, t.Run.Status)
	if t.Change != nil {
		fmt.Printf(" · change %s %q", changeName(t.ProjectSlug, t.Change.Number, t.Change.ID), t.Change.Title)
	}
	fmt.Println()
	for _, e := range t.Events {
		p, _ := json.Marshal(e.Payload)
		fmt.Printf("  %s  %-18s %s\n", strings.Replace(strings.TrimSuffix(e.At, "Z"), "T", " ", 1), e.Kind, string(p))
	}
	fmt.Println("\nfull detail: apptown trace", t.Run.ID, "--json")
	return nil
}

// cmdDiff prints a change's code against the default branch, or with -r a
// run's. A change is named as everywhere else (its id, <project>#<n>, or its
// number in the project); "-r" names a run. The two forms are never guessed
// from the argument: snake#7 is a change, and only -r makes it a run.
func cmdDiff(args []string) error {
	fs := newFlags("diff")
	project := fs.String("p", "", "project for a bare number")
	runFlag := fs.String("r", "", "a run instead of a change")
	pos := parseArgs(fs, args)
	path := ""
	switch {
	case *runFlag != "" && len(pos) == 0:
		id, err := runRef(*runFlag, *project)
		if err != nil {
			return err
		}
		path = "/runs/" + id.String() + "/diff"
	case *runFlag == "" && len(pos) == 1:
		id, err := changeRef(pos[0], *project)
		if err != nil {
			return err
		}
		path = "/changes/" + id.String() + "/diff"
	default:
		return usagef("usage: apptown diff <change> | apptown diff -r <run>")
	}
	diff, err := api.text(path)
	if err != nil {
		return err
	}
	fmt.Println(diff)
	return nil
}

// cmdWatch follows a run live: prints step status transitions and streams
// agent activity lines. Exits when the run is terminal or blocked on humans.
func cmdWatch(args []string) error {
	if len(args) < 1 {
		return usagef("usage: apptown watch <run-id>")
	}
	if err := runArg(args, ""); err != nil {
		return err
	}
	runID := args[0]
	lastStatus := map[ids.ID]string{}
	liveFrom := map[ids.ID]int{}
	lastState := ""
	fmt.Printf("watching run %s (ctrl-c to stop)\n", runID)
	for {
		var d runDetail
		if err := api.get("/runs/"+runID, &d); err != nil {
			return err
		}
		if d.Run.Status == "running" {
			if now := stateWords(d.State, d.StateDetail); now != lastState {
				lastState = now
				fmt.Printf("· now: %s\n", now)
			}
		}
		for _, s := range d.Steps {
			if lastStatus[s.ID] != s.Status {
				lastStatus[s.ID] = s.Status
				fmt.Printf("· [%s] %s (%s) → %s\n", s.Stage, s.Name, s.Type, s.Status)
			}
			if s.Type == "agent/pi" && s.Status == "running" {
				var live struct {
					Lines []string
					Total int
				}
				if err := api.get(fmt.Sprintf("/steps/%d/live?from=%d", s.ID, liveFrom[s.ID]), &live); err == nil {
					for _, l := range live.Lines {
						fmt.Printf("  %s\n", l)
					}
					liveFrom[s.ID] = live.Total
				}
			}
		}
		if d.Run.Status != "running" {
			fmt.Printf("run %s: %s\n", runID, d.Run.Status)
			return nil
		}
		// AppTown's own reading decides when following stops being
		// useful: a person's move or a block will not pass on its own.
		if d.State == "waiting on you" || d.State == "blocked" {
			fmt.Printf("run %s: %s — apptown status %s\n", runID, stateWords(d.State, d.StateDetail), runID)
			return nil
		}
		pause(2 * time.Second)
	}
}

// apptown timing [<workflow>] [-days N]   per step, across the runs of the window
// apptown timing -r <run>                 one run's timeline (an id or <project>#<number>)
func cmdTiming(args []string) error {
	fs := newFlags("timing")
	days := fs.Int("days", 7, "window in days")
	runRefFlag := fs.String("r", "", "one run's timeline instead: its id, <project>#<number>, or its number in the project")
	project := fs.String("p", "", "the project a bare -r number names a run in")
	rest := parseArgs(fs, args)
	if *runRefFlag != "" {
		runID, err := runRef(*runRefFlag, *project)
		if err != nil {
			return err
		}
		var tl struct {
			Run       ids.ID
			Workflow  string
			CreatedAt string
			Steps     []struct {
				Stage, Name, Type, Status string
				StartedAt, FinishedAt     string
				Ms, WaitMs, CostMicros    int64
			}
		}
		if err := api.get(fmt.Sprintf("/runs/%s/timing", runID), &tl); err != nil {
			return err
		}
		fmt.Printf("run %d (%s) created %s\n", tl.Run, tl.Workflow, tl.CreatedAt)
		w := tw()
		fmt.Fprintln(w, "STAGE\tSTEP\tTYPE\tSTATUS\tWAITED\tTOOK\tCOST")
		// Steps of one stage run side by side, so the run's length is
		// born-to-last-finish, not the sum of the steps.
		born, _ := time.Parse(time.RFC3339, tl.CreatedAt)
		var last time.Time
		for _, s := range tl.Steps {
			if t, err := time.Parse(time.RFC3339, s.FinishedAt); err == nil && t.After(last) {
				last = t
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Stage, s.Name, s.Type, s.Status, fmtMs(s.WaitMs), fmtMs(s.Ms), fmtMicros(s.CostMicros))
		}
		if !born.IsZero() && !last.IsZero() {
			fmt.Fprintf(w, "\t\t\t\t\t%s\tfrom open to the last step's end\n", fmtMs(last.Sub(born).Milliseconds()))
		}
		return w.Flush()
	}
	q := fmt.Sprintf("/timing?days=%d", *days)
	if len(rest) > 0 {
		q += "&workflow=" + rest[0]
	}
	var out struct {
		Days      int
		Workflows []struct {
			Workflow      string
			Runs          int
			TotalMedianMs int64
			Steps         []struct {
				Stage, Name, Type                                string
				Runs, Reworks                                    int
				MedianMs, P90Ms, MaxMs, WaitMedianMs, CostMicros int64
			}
		}
	}
	if err := api.get(q, &out); err != nil {
		return err
	}
	if len(out.Workflows) == 0 {
		fmt.Printf("no runs in the last %d days\n", out.Days)
		return nil
	}
	for i, wf := range out.Workflows {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("%s — %d runs in %d days, median %s from start to last step\n", wf.Workflow, wf.Runs, out.Days, fmtMs(wf.TotalMedianMs))
		w := tw()
		fmt.Fprintln(w, "STAGE\tSTEP\tTYPE\tRUNS\tWAIT (median)\tTOOK (median)\tP90\tMAX\tREWORKS\tCOST")
		for _, s := range wf.Steps {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%d\t%s\n", s.Stage, s.Name, s.Type, s.Runs,
				fmtMs(s.WaitMedianMs), fmtMs(s.MedianMs), fmtMs(s.P90Ms), fmtMs(s.MaxMs), s.Reworks, fmtMicros(s.CostMicros))
		}
		w.Flush()
	}
	return nil
}

func cmdAbandonRun(args []string) error {
	fs := newFlags("abandon-run")
	reason := fs.String("m", "", "reason")
	args = parseArgs(fs, args)
	if len(args) < 1 {
		return usagef("usage: apptown abandon-run <run-id> [-m reason]")
	}
	if err := runArg(args, ""); err != nil {
		return err
	}
	id := args[0]
	var res struct{ Abandoned []ids.ID }
	if err := api.post("/runs/"+id+"/abandon", map[string]any{"reason": *reason}, &res); err != nil {
		return err
	}
	fmt.Printf("abandoned run(s) %v\n", res.Abandoned)
	return nil
}

// cmdExecuting answers the question to ask before restarting an AppTown: is any
// step executing right now? A run waiting on a person survives a restart and
// is not listed. Exit 0: nothing is executing. Exit 3: something is, and
// each step is printed. Any other failure (AppTown could not answer) is
// the ordinary error exit, never read as idle.
func cmdExecuting(args []string) error {
	fs := newFlags("executing")
	parseArgs(fs, args)
	var out struct {
		Executing []struct {
			Project  string
			RunID    ids.ID
			ChangeID ids.ID
			Title    string
			StepID   ids.ID
			Step     string
			Type     string
			ElapsedS int
		}
		Operations []struct {
			Number     int64
			Kind       string
			Deployment string
			Step       string
			AskedBy    string
			ElapsedS   int
		}
	}
	if err := api.get("/runs/executing", &out); err != nil {
		return err
	}
	if len(out.Executing) == 0 && len(out.Operations) == 0 {
		fmt.Println("nothing is executing")
		return nil
	}
	w := tw()
	if len(out.Executing) > 0 {
		fmt.Fprintln(w, "PROJECT\tRUN\tSTEP\tTYPE\tFOR\tCHANGE")
		for _, s := range out.Executing {
			fmt.Fprintf(w, "%s\t%d\t%s (%d)\t%s\t%s\t%s\n", s.Project, s.RunID, s.Step, s.StepID, s.Type, time.Duration(s.ElapsedS)*time.Second, s.Title)
		}
	}
	if len(out.Operations) > 0 {
		if len(out.Executing) > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, "OPERATION\tKIND\tDEPLOYMENT\tSTEP\tFOR\tASKED BY")
		for _, o := range out.Operations {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", o.Number, o.Kind, o.Deployment, o.Step, time.Duration(o.ElapsedS)*time.Second, o.AskedBy)
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	exitProcess(3)
	return nil
}
