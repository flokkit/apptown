package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/strs"
)

// cmdChanges lists what's in flight, project-wide or everywhere; with
// --all, the recently shipped and abandoned too.
func cmdChanges(args []string) error {
	fs := newFlags("changes")
	project := fs.String("p", "", "project slug (default: all projects)")
	all := fs.Bool("all", false, "include recently shipped and abandoned changes")
	asJSON := fs.Bool("json", false, "print the changes as JSON")
	parseArgs(fs, args)
	slugs := []string{}
	if *project != "" {
		slugs = append(slugs, *project)
	} else {
		var ps []struct{ Slug string }
		if err := api.get("/projects", &ps); err != nil {
			return err
		}
		for _, p := range ps {
			slugs = append(slugs, p.Slug)
		}
	}
	if *asJSON {
		rows, err := changeRows(slugs, *all)
		if err != nil {
			return err
		}
		return printJSON(rows)
	}
	activity := runningActivity(*project)
	w := tw()
	fmt.Fprintln(w, "CHANGE\tPROJECT\tTITLE\tSTATE\tWHERE")
	rows := 0
	for _, slug := range slugs {
		var board struct {
			Columns []struct {
				State string
				Total int
				Items []boardItem
			}
		}
		if api.get("/projects/"+slug+"/board", &board) != nil {
			continue
		}
		for _, c := range board.Columns {
			closed := c.State == "shipped" || c.State == "abandoned"
			if closed && !*all {
				continue
			}
			for _, it := range c.Items {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", changeName(slug, it.Number, it.ID), slug, truncateStr(it.Title, 48), changeStateWords(c.State, it.ClosedAs), changeWhere(it, activity[it.ID], closed))
				rows++
			}
			if closed && c.Total > len(c.Items) {
				fmt.Fprintf(w, "%s\t%s\t\t%s\t\n", dim("…"), slug, dim(fmt.Sprintf("%d more %s", c.Total-len(c.Items), c.State)))
			}
		}
	}
	if rows == 0 {
		fmt.Fprintln(w, dim("(nothing in flight)\t\t\t\t"))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if !*all {
		fmt.Println(dim("shipped and abandoned changes: apptown changes --all"))
	}
	return nil
}

// changeRows is changes' --json: each project's board in column order, the
// shipped and abandoned columns only with all. A board that cannot be read
// is an error, never an empty list a script would read as nothing in flight.
func changeRows(slugs []string, all bool) (ChangeList, error) {
	out := ChangeList{Changes: []ChangeRow{}}
	for _, slug := range slugs {
		var board struct {
			Columns []struct {
				State string
				Items []boardItem
			}
		}
		if err := api.get("/projects/"+slug+"/board", &board); err != nil {
			return ChangeList{}, fmt.Errorf("reading %s's changes: %w", slug, err)
		}
		for _, c := range board.Columns {
			if (c.State == "shipped" || c.State == "abandoned") && !all {
				continue
			}
			for _, it := range c.Items {
				out.Changes = append(out.Changes, ChangeRow{Change: changeRefOf(slug, it.Number, it.ID), Title: it.Title, State: c.State, ClosedAs: it.ClosedAs})
			}
		}
	}
	return out, nil
}

// boardItem is one change on a project's board.
type boardItem struct {
	ID           ids.ID
	Number       int64
	Title        string
	Stage        string
	RunStatus    string
	WorkflowName string
	ClosedAs     string
}

// changeStateWords is a change's state as the change page says it: an
// abandoned change AppTown closed because its run found nothing to
// change (its recorded closedAs, never the reason's words) is closed.
func changeStateWords(state, closedAs string) string {
	if state == "abandoned" && closedAs == "nothing-to-change" {
		return "closed: nothing to change"
	}
	if state == "shipped" && closedAs == "shipped-with" {
		return "shipped with a later change"
	}
	return state
}

// runActivity is AppTown's reading of a running run: the words the
// change page shows ("waiting on you", "blocked", …) and their detail.
type runActivity struct {
	ID                 ids.ID
	State, StateDetail string
}

// runningActivity reads every running run's activity, by change: the
// change's root run, which reports a called workflow's state as its own.
func runningActivity(project string) map[ids.ID]runActivity {
	q := "/runs?status=running"
	if project != "" {
		q += "&project=" + project
	}
	var runs []struct {
		runActivity
		ChangeID ids.ID
	}
	out := map[ids.ID]runActivity{}
	if api.get(q, &runs) != nil {
		return out
	}
	for _, r := range runs {
		if have, ok := out[r.ChangeID]; !r.ChangeID.IsZero() && (!ok || r.ID.Compare(have.ID) < 0) {
			out[r.ChangeID] = r.runActivity
		}
	}
	return out
}

// stateWords is a running run's activity as the change page says it
// ("waiting on you: accept"), or "unknown" when AppTown gave none: a
// stage name is the workflow's own label, not what the run is doing.
func stateWords(state, detail string) string {
	if state == "" {
		return "unknown"
	}
	return strings.TrimSuffix(state+": "+detail, ": ")
}

// changeWhere says where a change is: AppTown's own state when a run is
// going ("waiting on you: accept", "blocked: tests (fail)"), else its
// workflow and stage. The stage name alone ("apply", "fix") once hid that
// a change was waiting on its reader (sweep 7).
func changeWhere(it boardItem, act runActivity, closed bool) string {
	switch {
	case closed:
		return "-"
	case act.State == "working" || act.State == "agent working":
		return it.WorkflowName + " · " + it.Stage
	case act.State != "":
		return stateWords(act.State, act.StateDetail)
	case it.RunStatus == "running":
		return it.WorkflowName + " · " + it.Stage
	case it.RunStatus != "" && it.RunStatus != "done":
		return it.WorkflowName + " · " + it.RunStatus
	}
	return "idle"
}

// cmdShow: the cockpit in the terminal — what the change is FOR, where it's
// at, its runs with roles, and what to do next.
func cmdShow(args []string) error {
	fs := newFlags("show")
	asJSON := fs.Bool("json", false, "print the change as JSON")
	args = parseArgs(fs, args)
	if len(args) < 1 {
		return fmt.Errorf("usage: apptown show <change-id> [--json]")
	}
	id, err := changeRef(args[0], "")
	if err != nil {
		return err
	}
	var view struct {
		ProjectSlug string
		Change      struct {
			ID          ids.ID
			Number      int64
			Title       string
			Body        string
			State       string
			ClosedAs    string
			Branch      string
			ExternalRef string
			Summary     string
			Provenance  string
		}
		Diffstat    string
		Report      string
		State       string
		StateDetail string
		Runs        []struct {
			ID           ids.ID
			Number       int64
			WorkflowName string
			Role         string
			Status       string
			CurrentStage string
		}
		Commits []struct{ SHA, Subject, When string }
		// Head is the change's current commit; Detail its latest run.
		Head   string
		Detail *struct {
			Steps []cockpitStep `json:"steps"`
		}
		// Approval is the approval the change waits on, with what its
		// commit adds.
		Approval *struct {
			StepID    ids.ID
			Adds      *changeAdds
			AddsError string
		}
		Usage *struct {
			Total      int64
			CostMicros int64
		}
	}
	if err := api.get(fmt.Sprintf("/changes/%d", id), &view); err != nil {
		return err
	}
	ch := view.Change
	if *asJSON {
		d := ChangeDetail{Change: changeRefOf(view.ProjectSlug, ch.Number, ch.ID), Title: ch.Title, State: ch.State, ClosedAs: ch.ClosedAs,
			StateDetail: view.StateDetail, Branch: ch.Branch, Runs: []RunDetail{}, Commits: []Commit{}}
		for _, r := range view.Runs {
			d.Runs = append(d.Runs, RunDetail{Run: RunRef{ID: r.ID, Number: r.Number}, Workflow: r.WorkflowName,
				Role: r.Role, Status: r.Status, Stage: r.CurrentStage})
		}
		for _, c := range view.Commits {
			d.Commits = append(d.Commits, Commit{SHA: c.SHA, Subject: c.Subject})
		}
		if view.Approval != nil && !view.Approval.StepID.IsZero() {
			d.WaitingOn = &Waiting{Step: view.Approval.StepID}
		}
		return printJSON(d)
	}
	fmt.Printf("%s %s  %s\n", bold("change "+changeName(view.ProjectSlug, ch.Number, ch.ID)), ch.Title, stateChip(changeStateWords(ch.State, ch.ClosedAs)))
	meta := []string{view.ProjectSlug}
	if ch.Branch != "" {
		meta = append(meta, ch.Branch)
	}
	if view.Diffstat != "" {
		meta = append(meta, strings.TrimSpace(view.Diffstat))
	}
	if ch.ExternalRef != "" {
		meta = append(meta, ch.ExternalRef)
	}
	if ch.Provenance != "" {
		meta = append(meta, yellow(ch.Provenance+" — reconstructed evidence"))
	}
	if view.Usage != nil && view.Usage.Total > 0 {
		meta = append(meta, fmt.Sprintf("%dk tok · $%.2f", view.Usage.Total/1000, float64(view.Usage.CostMicros)/1e6))
	}
	fmt.Println(dim(strings.Join(meta, " · ")))
	if strings.TrimSpace(ch.Body) != "" {
		fmt.Printf("\n%s\n%s\n", bold("For"), indentBody(ch.Body, 20))
	}
	if now := nowLine(ch.ID, view.State, view.StateDetail); now != "" {
		fmt.Printf("\n%s %s\n", bold("Now"), now)
	}
	if a := view.Approval; a != nil {
		switch {
		case a.AddsError != "":
			fmt.Printf("\n%s\n  %s what the change adds could not be read: %s\n", bold("This change adds"), yellow("?"), a.AddsError)
		case a.Adds != nil:
			fmt.Println()
			printAdds(os.Stdout, a.Adds, "")
		}
		if view.Detail != nil {
			if line := screenshotsLine(latestScreenshots(view.Detail.Steps), view.Head, fmt.Sprintf("%s/changes/%s", api.base, ch.ID)); line != "" {
				fmt.Printf("\n%s\n", line)
			}
		}
	}
	if s := strs.FirstNonBlank(ch.Summary, view.Report); strings.TrimSpace(s) != "" {
		fmt.Printf("\n%s\n%s\n", bold(storyHeading(ch.State)), indentBody(s, 24))
	}
	if len(view.Runs) > 0 {
		fmt.Printf("\n%s\n", bold("Runs"))
		w := tw()
		for _, r := range view.Runs {
			role := r.Role
			if role == "" {
				role = "-"
			}
			fmt.Fprintf(w, "  %d\t%s\t%s\t%s\t%s\n", r.ID, r.WorkflowName, role, r.Status, r.CurrentStage)
		}
		w.Flush()
		// Each run's tests, when their reading is not a plain pass: skipped
		// tests, or counts not known.
		details := make([]runDetail, 0, len(view.Runs))
		for _, r := range view.Runs {
			var d runDetail
			if api.get(fmt.Sprintf("/runs/%d", r.ID), &d) == nil {
				details = append(details, d)
			}
		}
		if lines := testsLines(details); len(lines) > 0 {
			fmt.Printf("\n%s\n", bold("Tests"))
			for _, l := range lines {
				fmt.Println("  " + l)
			}
		}
		last := view.Runs[len(view.Runs)-1]
		if last.Status != "done" {
			var d loopRunDetail
			if api.get(fmt.Sprintf("/runs/%d", last.ID), &d) == nil {
				printDisputeOf(d)
			}
		}
	}
	var preview previewStatus
	if api.get(fmt.Sprintf("/changes/%d/preview", id), &preview) == nil {
		if line := previewLine(preview); line != "" {
			fmt.Printf("\n%s %s\n", bold("Preview"), line)
		}
	}
	if len(view.Commits) > 0 {
		fmt.Printf("\n%s\n", bold("Commits"))
		for i, c := range view.Commits {
			if i >= 8 {
				fmt.Println(dim(fmt.Sprintf("  … %d more", len(view.Commits)-8)))
				break
			}
			fmt.Printf("  %s %s %s\n", c.SHA, c.Subject, dim(c.When))
		}
	}
	switch ch.State {
	case "in_progress":
		fmt.Printf("\n%s apptown follow %d · apptown preview %d · apptown land %d · apptown invoke -c %d -w <workflow>\n", dim("next:"), ch.ID, ch.ID, ch.ID, ch.ID)
	case "proposed":
		fmt.Printf("\n%s apptown invoke -c %d -w <workflow>\n", dim("next:"), ch.ID)
	}
	return nil
}

// nowLine is what the change is doing, in AppTown's words, with the move
// that answers it: a change waiting on an approval once showed only its
// stage name (sweep 7).
func nowLine(changeID ids.ID, state, detail string) string {
	words := strings.TrimSuffix(state+": "+detail, ": ")
	switch state {
	case "", "done", "abandoned":
		return ""
	case "waiting on you":
		return yellow(words) + dim(fmt.Sprintf(" — apptown follow %d", changeID))
	case "blocked":
		return red(words) + dim(fmt.Sprintf(" — apptown follow %d for next steps", changeID))
	}
	return words
}

// storyHeading names the change's story: "So far" while it is open, what
// it became once it is closed (a shipped change's report read "So far").
func storyHeading(state string) string {
	switch state {
	case "shipped":
		return "What shipped"
	case "abandoned":
		return "Where it stopped"
	}
	return "So far"
}

// cmdHome: bare `apptown status` — the operator's world in one screen:
// what's waiting on you, what's running, what shipped lately.
func cmdHome() error {
	var dash struct {
		PendingApprovals []struct {
			StepID       ids.ID
			StepName     string
			RunID        ids.ID
			ChangeID     ids.ID
			ChangeNumber int64
			ItemTitle    string
			ProjectSlug  string
		}
		ActiveRuns []struct {
			RunID        ids.ID
			ChangeID     ids.ID
			ChangeNumber int64
			ItemTitle    string
			Workflow     string
			State        string
			StateDetail  string
			ProjectSlug  string
		}
	}
	if err := api.get("/dashboard", &dash); err != nil {
		return err
	}
	if len(dash.PendingApprovals) > 0 {
		fmt.Println(bold("Waiting on you"))
		for _, p := range dash.PendingApprovals {
			name := changeName(p.ProjectSlug, p.ChangeNumber, p.ChangeID)
			fmt.Printf("  %s %s %s %s %s\n", yellow("●"), name,
				truncateStr(p.ItemTitle, 48), dim(p.ProjectSlug+" · "+p.StepName),
				dim("→ apptown follow "+name))
		}
	} else {
		fmt.Println(dim("Nothing waiting on you."))
	}
	if len(dash.ActiveRuns) > 0 {
		fmt.Println(bold("\nIn flight"))
		for _, r := range dash.ActiveRuns {
			fmt.Printf("  %s %s %s %s\n", cyan("⧗"), changeName(r.ProjectSlug, r.ChangeNumber, r.ChangeID),
				truncateStr(r.ItemTitle, 48), dim(r.ProjectSlug+" · "+r.Workflow+" · "+stateWords(r.State, r.StateDetail)))
		}
	}
	fmt.Printf("\n%s\n", dim("apptown changes · apptown show <id> · apptown follow <id>"))
	return nil
}

// cmdImport: adopt an outside branch as a Change — dark work made visible,
// graded honestly (reconstructed, not witnessed).
func cmdImport(args []string) error {
	fs := newFlags("import")
	project := fs.String("p", "", "project slug")
	title := fs.String("t", "", "change title (default: import <branch>)")
	pos := parseArgs(fs, args)
	if len(pos) < 1 {
		return fmt.Errorf("usage: apptown import <branch> [-p project] [-t title]")
	}
	branch := pos[0]
	slug, err := resolveProject(*project)
	if err != nil {
		return err
	}
	var out struct {
		Change struct {
			ID      ids.ID
			Number  int64
			Branch  string
			BaseSHA string `json:"baseSha"`
		}
	}
	if err := api.post("/projects/"+slug+"/import", map[string]string{"branch": branch, "title": *title}, &out); err != nil {
		return err
	}
	name := changeName(slug, out.Change.Number, out.Change.ID)
	fmt.Printf("%s change %s adopts %s %s\n", bold("▶"), bold(name),
		cyan(branch), dim("(evidence: commits only — reconstructed, not witnessed)"))
	fmt.Printf("  next: apptown show %s · apptown invoke -c %s -w <workflow> · apptown land %s\n", name, name, name)
	return nil
}

// landWhenClear posts the landing, and if refused because another change
// is landing (one landing at a time per project), WAITS ITS TURN — polling
// until the track clears, then landing. The queue is the humans' terminals,
// not engine state: Ctrl-C leaves the change untouched, and the printed
// line says exactly what it's waiting for.
func landWhenClear(changeID ids.ID, progress io.Writer) (json.RawMessage, error) {
	waited := false
	for {
		raw, err := api.request("POST", fmt.Sprintf("/changes/%d/land", changeID), map[string]string{})
		if err == nil {
			if waited {
				fmt.Fprintf(progress, "%s no other landing is running — landing now\n", green("✓"))
			}
			return raw, nil
		}
		if refusalCode(err) != "landing-occupied" {
			return nil, err
		}
		if !waited {
			fmt.Fprintf(progress, "%s %s\n", yellow("⏳"), err.Error())
			fmt.Fprintln(progress, dim("  waiting for the other landing to finish (Ctrl-C stops waiting; the change is kept)"))
			waited = true
		}
		pause(landingRetryEvery)
	}
}

// landingRetryEvery is how long landWhenClear waits before asking again.
var landingRetryEvery = 5 * time.Second

// Stuck-state verbs. A blocked run left people with raw API calls ("Do I
// abandon it? Retry it? Delete it forever? How?"). Three moves, three verbs. Deletion is not one of them: changes are
// abandoned with the record kept — the ledger is the point.

// nextAct is one act AppTown offers on a run, with the step it acts on
// (workflow.NextAct).
type nextAct struct {
	Act    string
	StepID ids.ID
	Step   string
}

// actOf is the first act of kind act in acts.
func actOf(acts []nextAct, act string) (nextAct, bool) {
	for _, a := range acts {
		if a.Act == act {
			return a, true
		}
	}
	return nextAct{}, false
}

// changeNext reads the change's current run, as AppTown chose it, and the
// acts AppTown offers on it. The client picks no run and no step: the
// override once landed on the working run while the landing was the one
// wedged (complaint #68).
func changeNext(changeID ids.ID) (runID ids.ID, acts []nextAct, landing bool, err error) {
	var view struct {
		Change struct{ State string }
		Detail *struct {
			Run  struct{ ID ids.ID }
			Next []nextAct
		}
	}
	if err = api.get(fmt.Sprintf("/changes/%d", changeID), &view); err != nil {
		return ids.Nil, nil, false, err
	}
	if view.Detail == nil {
		return ids.Nil, nil, false, fmt.Errorf("change %d has no runs", changeID)
	}
	return view.Detail.Run.ID, view.Detail.Next, view.Change.State == "landing", nil
}

// cmdRetry: re-dispatch the stage's producing agent — the machine loop gets
// another round with ${{feedback.latest}} — then re-attach the loop.
func cmdRetry(args []string) error {
	fs := newFlags("retry")
	yes := fs.Bool("y", false, "auto-approve gates and landing")
	noFollow := fs.Bool("no-follow", false, "dispatch the rework and return")
	cont := fs.Bool("continue", false, "resume the agent's previous session (it keeps its memory; feedback is the new message)")
	msg := fs.String("m", "", "send-back note — what to change; becomes the resumed agent's feedback")
	fs.BoolVar(cont, "c", *cont, "shorthand for --continue")
	restart := fs.Bool("restart", false, "start over: reset the run's work to its base, fresh session")
	acceptUncopied := fs.Bool("accept-uncopied-data", false, "a deploy refused because the copy taken before its release was incomplete: release anyway (the attempt records your choice)")
	pos := parseArgs(fs, args)
	if len(pos) < 1 {
		return fmt.Errorf("usage: apptown retry <change-id> [-c|--continue | --restart | --accept-uncopied-data] [-y] [--no-follow]")
	}
	if *cont && *restart {
		return fmt.Errorf("--continue and --restart cannot be used together")
	}
	changeID, err := changeRef(pos[0], "")
	if err != nil {
		return err
	}
	runID, acts, landing, err := changeNext(changeID)
	if err != nil {
		return err
	}
	if *acceptUncopied {
		return retryDeployAcceptingUncopiedData(changeID, runID, *yes, *noFollow)
	}
	rework, ok := actOf(acts, "rework")
	if !ok {
		// A landing wedged at a gate has no agent to give another round: the
		// implementation is one run back, and the way to it is a send-back
		// with a note. With -m, whose help calls it a send-back note, do it.
		if *msg != "" {
			return sendBack(changeID, *msg, *yes, *noFollow)
		}
		blocker, _ := actOf(acts, "override")
		return fmt.Errorf("%s", retryHint(changeID, runID, blocker.StepID, blocker.Step, landing))
	}
	mode := ""
	how := "fresh session, workspace as it stands"
	if *cont {
		mode, how = "continue", "resuming the agent's previous session"
	}
	if *restart {
		mode, how = "restart", "workspace reset to the run's base, fresh session"
	}
	payload := map[string]string{}
	if mode != "" {
		payload["mode"] = mode
	}
	if *msg != "" {
		payload["feedback"] = *msg
	}
	var body any
	if len(payload) > 0 {
		body = payload
	}
	if _, err := api.request("POST", fmt.Sprintf("/steps/%d/run", rework.StepID), body); err != nil {
		return err
	}
	fmt.Printf("%s rework dispatched (%s)\n", green("↻"), how)
	return followOrReturn(changeID, runID, *yes, *noFollow)
}

// retryDeployAcceptingUncopiedData retries the run's refused deploy step,
// releasing over data AppTown could not copy before it: a person's own
// choice, which AppTown records with the attempt.
func retryDeployAcceptingUncopiedData(changeID, runID ids.ID, yes, noFollow bool) error {
	var d runDetail
	if err := api.get(fmt.Sprintf("/runs/%d", runID), &d); err != nil {
		return err
	}
	var step ids.ID
	for _, s := range d.Steps {
		if s.Type == "deploy" && s.Status != "pass" && s.Status != "pending" && s.Status != "running" {
			step = s.ID
		}
	}
	if step.IsZero() {
		return fmt.Errorf("change %d has no refused deploy step to retry accepting uncopied data", changeID)
	}
	if _, err := api.request("POST", fmt.Sprintf("/steps/%d/run", step), map[string]any{"acceptUncopiedData": true}); err != nil {
		return err
	}
	fmt.Printf("%s deploy retried, releasing over data AppTown could not copy (recorded)\n", green("↻"))
	return followOrReturn(changeID, runID, yes, noFollow)
}

// cmdOverride: a human overrules the blocking machine verdict, with a
// mandatory reason. Recorded, attributed, never silent.
func cmdOverride(args []string) error {
	fs := newFlags("override")
	yes := fs.Bool("y", false, "auto-approve gates and landing")
	noFollow := fs.Bool("no-follow", false, "record the override and return")
	pos := parseArgs(fs, args)
	if len(pos) < 1 {
		return fmt.Errorf(`usage: apptown override <change-id> "<justification>" [-y] [--no-follow]`)
	}
	reason := strings.TrimSpace(strings.Join(pos[1:], " "))
	if reason == "" {
		return fmt.Errorf(`an override requires a justification: apptown override <change-id> "<why>"`)
	}
	changeID, err := changeRef(pos[0], "")
	if err != nil {
		return err
	}
	runID, acts, _, err := changeNext(changeID)
	if err != nil {
		return err
	}
	blocker, ok := actOf(acts, "override")
	if !ok {
		return fmt.Errorf("run %d has no failed check to override", runID)
	}
	if _, err := api.request("POST", fmt.Sprintf("/steps/%d/override", blocker.StepID),
		map[string]string{"reason": reason}); err != nil {
		return err
	}
	fmt.Printf("%s %s overridden by %s — reason recorded\n", yellow("⚑"), blocker.Step, api.actor)
	_, _ = api.request("POST", fmt.Sprintf("/runs/%d/advance", runID), nil)
	return followOrReturn(changeID, runID, *yes, *noFollow)
}

// cmdAbandon: close the change. The runs stop; the record stays.
func cmdAbandon(args []string) error {
	pos := parseArgs(newFlags("abandon"), args)
	if len(pos) < 1 {
		return fmt.Errorf(`usage: apptown abandon <change-id> ["<reason>"]`)
	}
	reason := strings.TrimSpace(strings.Join(pos[1:], " "))
	changeID, err := changeRef(pos[0], "")
	if err != nil {
		return err
	}
	if _, err := api.request("POST", fmt.Sprintf("/changes/%d/abandon", changeID),
		map[string]string{"reason": reason}); err != nil {
		return err
	}
	fmt.Printf("%s change %s abandoned — record retained (apptown show %s)\n", yellow("⏹"), pos[0], pos[0])
	return nil
}

// printDispute lays the disagreement on the table so a human can
// adjudicate it (a person needs some way to see what the disagreement
// is). For every failed machine verdict in the current stage:
// the reviewer's case in full, the implementer's claim, and where to see
// the code. Symmetry is deliberate — the human is judging a dispute
// between two parties, not reading an error log.
func printDispute(runID ids.ID) bool {
	var d loopRunDetail
	if api.get(fmt.Sprintf("/runs/%d", runID), &d) != nil {
		return false
	}
	return printDisputeOf(d)
}

func printDisputeOf(d loopRunDetail) bool {
	text := disputeText(d)
	if text == "" {
		return false
	}
	fmt.Print(text)
	// The other side: the implementer's most recent claim.
	if i := claimOf(d); i >= 0 {
		a := d.Artifacts[i]
		var art struct{ Body string }
		if api.get(fmt.Sprintf("/artifacts/%d", a.ID), &art) == nil && art.Body != "" {
			fmt.Printf("\n  %s the implementer's claim (%s):\n", cyan("⇄"), a.Kind)
			fmt.Println(indentBody(art.Body, 76))
		}
	}
	fmt.Printf("\n%s\n", dim(fmt.Sprintf("  the code itself: apptown diff -r %s", d.Run.ID)))
	return true
}

// claimOf is the index of the implementer's report among a run's
// artifacts: the newest of a kind one of the run's review steps declares
// it audits ("of"), as the approval brief reads it (workflow.ReadClaimIn);
// -1 when there is none. The declaration decides, never a kind's
// spelling: the newest "*report" was once a vulnerability scan's.
func claimOf(d loopRunDetail) int {
	audited := map[string]bool{}
	for _, s := range d.Steps {
		if of, _ := s.Config["of"].(string); s.Type == "agent/review" && of != "" {
			audited[of] = true
		}
	}
	for i := len(d.Artifacts) - 1; i >= 0; i-- {
		if audited[d.Artifacts[i].Kind] {
			return i
		}
	}
	return -1
}

// disputeText is every failed machine verdict in the current stage, each
// output once: a build log several steps report (a preview's boot, the
// tests' build) printed in full for each one buried the disagreement, so a
// repeated output names the step that showed it, and a long one shows its
// first line and its tail, where a build's error is.
func disputeText(d loopRunDetail) string {
	var b strings.Builder
	shown := map[string]string{} // output → the step that showed it
	show := func(step, text string, render func(string) string) {
		key := strings.TrimSpace(text)
		if first, ok := shown[key]; ok {
			b.WriteString(dim(fmt.Sprintf("    (the same output as %s, above)", first)) + "\n")
			return
		}
		shown[key] = step
		b.WriteString(render(text) + "\n")
	}
	for _, s := range d.Steps {
		if s.Stage != d.Run.CurrentStage || s.Type == "approval" {
			continue
		}
		if s.Status != "fail" && s.Status != "error" {
			continue
		}
		if b.Len() == 0 {
			fmt.Fprintf(&b, "\n%s\n", bold("The disagreement"))
		}
		who := "machine check"
		switch s.Type {
		case "agent/review":
			who = "reviewer"
		case "preview":
			who = "boot check"
		case "scan":
			who = "scanner"
		}
		// A review the reviewer passed and AppTown's own file check
		// failed is AppTown's failure, not the reviewer's rejection.
		reviewerPassed, _ := s.Evidence["reviewerSummary"].(string)
		if reviewerPassed != "" {
			who = "AppTown's file check"
		}
		fmt.Fprintf(&b, "  %s %s (%s, %s):\n", red("✗"), s.Name, who, s.Status)
		if sum, ok := s.Evidence["summary"].(string); ok && sum != "" {
			show(s.Name, sum, func(t string) string { return excerpt(t, 76) })
		} else if reason, ok := s.Evidence["reason"].(string); ok && reason != "" {
			show(s.Name, reason, func(t string) string { return excerpt(t, 40) })
		}
		if reviewerPassed != "" {
			b.WriteString(indentBody("the reviewer itself passed it: "+reviewerPassed, 74) + "\n")
		}
		if fs, ok := s.Evidence["findings"].([]any); ok {
			for i, f := range fs {
				b.WriteString(indentBody(fmt.Sprintf("%d. %v", i+1, f), 74) + "\n")
			}
		}
		if lt, ok := s.Evidence["logTail"].(string); ok && lt != "" {
			show(s.Name, lt, func(t string) string { return dim(excerpt(t, 40)) })
		}
	}
	return b.String()
}

// excerpt indents s, keeping its first line and its last tail lines when
// it is longer: an error's sentence leads, and a log's end says what broke.
func excerpt(s string, tail int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= tail+1 {
		return indentBody(s, len(lines))
	}
	kept := append([]string{lines[0], dim(fmt.Sprintf("… (%d lines omitted)", len(lines)-1-tail))}, lines[len(lines)-tail:]...)
	return indentBody(strings.Join(kept, "\n"), len(kept))
}

// previewStatus is the preview owner's answer to whether a change's
// preview is serving (GET /changes/{id}/preview, what the page's try-it
// button follows).
type previewStatus struct {
	State, URL, Reason string
}

// previewLine is what show says about the change's preview: its address
// only while the preview owner says it is live. A step's recorded address
// outlives the preview, and show once printed one nobody was serving
// (sweep 7, A3).
func previewLine(p previewStatus) string {
	switch p.State {
	case "live":
		return p.URL
	case "starting":
		return dim("starting…")
	}
	return ""
}

// retryHint says why retry cannot help — AppTown offers no agent round on
// the run now — and names the doors that can: on a landing, running its
// failed gates again after a fix (apptown recheck); on any run, a send-back.
func retryHint(changeID, runID, blockerID ids.ID, blockerName string, landing bool) string {
	recheck := ""
	if landing {
		recheck = fmt.Sprintf("Once what failed is fixed, run the landing's failed gates again:\n  apptown recheck %d\nOr send", changeID)
	}
	if !blockerID.IsZero() {
		if recheck == "" {
			recheck = "Send"
		}
		return fmt.Sprintf("change %d is blocked at %s (%d) in run %d, and AppTown offers no agent round to retry there.\n%s it back with a note for the agent:\n  apptown sendback %d \"what to change\"", changeID, blockerName, blockerID, runID, recheck, changeID)
	}
	if recheck != "" {
		return fmt.Sprintf("AppTown offers no agent round to retry on run %d now (apptown status %d says what it is doing).\n%s the change back if it needs a code change:\n  apptown sendback %d \"what to change\"", runID, runID, recheck, changeID)
	}
	return fmt.Sprintf("AppTown offers no agent round to retry on run %d now (apptown status %d says what it is doing); if the change needs a code change, send it back:\n  apptown sendback %d \"what to change\"", runID, runID, changeID)
}

// cmdSendBack: the CLI's twin of the page's "Send it back with a note".
// The server closes a wedged landing, puts the change back in progress and
// starts a rework round with the note as the agent's brief.
func cmdSendBack(args []string) error {
	fs := newFlags("sendback")
	yes := fs.Bool("y", false, "auto-approve gates and landing")
	noFollow := fs.Bool("no-follow", false, "send it back and return")
	pos := parseArgs(fs, args)
	if len(pos) < 1 {
		return fmt.Errorf(`usage: apptown sendback <change-id> "<what to change>" [-y] [--no-follow]`)
	}
	note := strings.TrimSpace(strings.Join(pos[1:], " "))
	if note == "" {
		return fmt.Errorf(`say what to change — the note is what the agent works from: apptown sendback <change-id> "<note>"`)
	}
	changeID, err := changeRef(pos[0], "")
	if err != nil {
		return err
	}
	return sendBack(changeID, note, *yes, *noFollow)
}

func sendBack(changeID ids.ID, note string, yes, noFollow bool) error {
	var out struct {
		RunID  ids.ID `json:"runId"`
		StepID ids.ID `json:"stepId"`
	}
	raw, err := api.request("POST", fmt.Sprintf("/changes/%d/sendback", changeID), map[string]string{"note": note})
	if err != nil {
		return err
	}
	_ = json.Unmarshal(raw, &out)
	fmt.Printf("%s sent back — rework run %d started from your note\n", green("↩"), out.RunID)
	return followOrReturn(changeID, out.RunID, yes, noFollow)
}

// followOrReturn stays with the change, as every loop command does, unless
// --no-follow asked it to fire and return; then it says how to come back.
func followOrReturn(changeID, runID ids.ID, yes, noFollow bool) error {
	if noFollow {
		fmt.Printf("  follow: apptown follow %d\n", changeID)
		return nil
	}
	fmt.Println(dim("  staying with it (Ctrl-C leaves it running)"))
	return followChange(changeID, runID, "", yes)
}

// cmdStart opens a Change with a branch + worktree and no run — work needs
// a place to happen before it needs a workflow (design/subsystems/dev-loop.md).
func cmdStart(args []string) error {
	fs := newFlags("start")
	project := fs.String("p", "", "project slug")
	title := fs.String("t", "", "change title")
	typ := fs.String("type", "feature", "change type")
	body := fs.String("body", "", "change description")
	ref := fs.String("ref", "", "external reference (Jira key, ticket URL)")
	parseArgs(fs, args)
	if *project == "" || *title == "" {
		return fmt.Errorf("-p and -t are required")
	}
	raw, err := api.request("POST", "/projects/"+*project+"/changes",
		map[string]string{"title": *title, "type": *typ, "body": *body, "externalRef": *ref})
	if err != nil {
		return err
	}
	var out struct {
		Change struct {
			ID     ids.ID
			Number int64
			Branch string
		}
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	name := changeName(*project, out.Change.Number, out.Change.ID)
	fmt.Printf("change %s opened", name)
	if out.Change.Branch != "" {
		fmt.Printf(" on %s", out.Change.Branch)
	}
	fmt.Printf("\nnext: apptown invoke -c %s -w <workflow> · apptown land %s\n", name, name)
	return nil
}

// cmdRecheck: after fixing what a landing gate rejected, re-run the
// change's FAILED landing gates and follow the landing as land does. Bare,
// inside a change chat, it rechecks that chat's change through the chat's
// door (the session token opens only that one). Approvals stay with a person.
func cmdRecheck(args []string) error {
	fs := newFlags("recheck")
	yes := fs.Bool("y", false, "auto-approve landing gates")
	noFollow := fs.Bool("no-follow", false, "start the recheck and return")
	args = parseArgs(fs, args)
	var out struct {
		RunID   ids.ID   `json:"runId"`
		Rerun   []string `json:"rerun"`
		Message string   `json:"message"`
	}
	if len(args) == 0 {
		sid, tok := os.Getenv("APPTOWN_SESSION_ID"), os.Getenv("APPTOWN_SESSION_TOKEN")
		if sid == "" || tok == "" {
			return fmt.Errorf("usage: apptown recheck <change-id> [-y] [--no-follow] (bare, inside a change chat)")
		}
		if err := api.post("/console/"+sid+"/recheck", map[string]any{}, &out); err != nil {
			return err
		}
		fmt.Println(out.Message)
		return nil
	}
	id, err := changeRef(args[0], "")
	if err != nil {
		return err
	}
	var view struct {
		ProjectSlug string
		Change      struct{ Number int64 }
	}
	_ = api.get(fmt.Sprintf("/changes/%s", id), &view)
	name := changeName(view.ProjectSlug, view.Change.Number, id)
	if err := api.post(fmt.Sprintf("/changes/%s/recheck", id), map[string]any{}, &out); err != nil {
		return err
	}
	fmt.Println(out.Message)
	if len(out.Rerun) == 0 {
		return nil
	}
	if *noFollow {
		fmt.Printf("  follow: apptown follow %s\n", name)
		return nil
	}
	status, err := streamRunFor(out.RunID, id, *yes)
	if err != nil {
		return err
	}
	if status != "done" {
		printLandingStopped(name, status)
		return nil
	}
	printShipped(name, view.ProjectSlug, out.RunID)
	return nil
}

// cmdLand launches the landing workflow — the only path to merge+ship+release
// — and stays with it: gates stream, approvals are taken inline.
func cmdLand(args []string) error {
	fs := newFlags("land")
	yes := fs.Bool("y", false, "auto-approve landing gates")
	noFollow := fs.Bool("no-follow", false, "fire the landing and return")
	asJSON := fs.Bool("json", false, "print the landing run as JSON and return (no following)")
	args = parseArgs(fs, args)
	if len(args) < 1 {
		// Inside a change chat, bare `apptown land` lands THIS session's own
		// change through the console plane — the session token opens only
		// that one door. Checks and the person's OK still stand.
		if sid, tok := os.Getenv("APPTOWN_SESSION_ID"), os.Getenv("APPTOWN_SESSION_TOKEN"); sid != "" && tok != "" {
			// The chat's door answers with the landing's run alone, not the
			// change; --json names both, so it is refused here, before
			// anything starts, rather than printed half.
			if *asJSON {
				return fmt.Errorf("a bare apptown land in a chat answers in words, not --json: its door names the landing run but not the change; run it without --json")
			}
			if err := api.post("/console/"+sid+"/land", map[string]any{}, nil); err != nil {
				return err
			}
			fmt.Println("landing started — AppTown runs its checks, then asks a person for the final OK")
			return nil
		}
		return fmt.Errorf("usage: apptown land <change-id> [-y] [--no-follow]")
	}
	id, err := changeRef(args[0], "")
	if err != nil {
		return err
	}
	var view struct {
		ProjectSlug string
		Change      struct{ Number int64 }
	}
	_ = api.get(fmt.Sprintf("/changes/%s", id), &view)
	name := changeName(view.ProjectSlug, view.Change.Number, id)
	progress := io.Writer(os.Stdout)
	if *asJSON {
		progress = os.Stderr
	}
	raw, err := landWhenClear(id, progress)
	if err != nil {
		return err
	}
	var out struct {
		Run struct {
			ID     ids.ID
			Number int64
		}
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	if *asJSON {
		return printJSON(LandingRequested{Change: changeRefOf(view.ProjectSlug, view.Change.Number, id),
			Run: RunRef{ID: out.Run.ID, Number: out.Run.Number}})
	}
	fmt.Printf("%s landing change %s · run %s\n", bold("▶"), name, out.Run.ID)
	if *noFollow {
		fmt.Printf("  follow: apptown follow %s\n", name)
		return nil
	}
	status, err := streamRunFor(out.Run.ID, id, *yes)
	if err != nil {
		return err
	}
	if status != "done" {
		printLandingStopped(name, status)
		return nil
	}
	printShipped(name, view.ProjectSlug, out.Run.ID)
	return nil
}

// printLandingStopped says where a landing that did not ship left its
// change. A blocked landing still holds it: the change stays landing until
// the failed gate is fixed and rechecked, or the change is sent back. An
// ended one (a branch behind main ends it) put the change back in progress.
func printLandingStopped(name, status string) { fmt.Println(landingStopped(name, status)) }

func landingStopped(name, status string) string {
	if status == "blocked" {
		return fmt.Sprintf("%s the change is still landing: fix what failed, then run its failed gates again: apptown recheck %s; or send it back: apptown sendback %s \"what to change\"", yellow("⏸"), name, name)
	}
	return fmt.Sprintf("%s landing ended %s — the change is back in progress; land it again: apptown land %s", yellow("⏸"), status, name)
}

func cmdInvoke(args []string) error {
	fs := newFlags("invoke")
	project := fs.String("p", "", "project slug")
	workflow := fs.String("w", "", "workflow name")
	title := fs.String("t", "", "change title")
	typ := fs.String("type", "feature", "change type")
	body := fs.String("body", "", "change description")
	intent := fs.String("intent", "", "why this work exists (in a chat, recorded with the change)")
	changeID := fs.String("c", "", "existing change id to run the workflow on")
	parseArgs(fs, args)
	if *changeID != "" {
		if *workflow == "" {
			return fmt.Errorf("-w is required with -c")
		}
		id, err := changeRef(*changeID, *project)
		if err != nil {
			return err
		}
		raw, err := api.request("POST", fmt.Sprintf("/changes/%d/runs", id), map[string]string{"workflow": *workflow})
		if err != nil {
			return err
		}
		var out struct {
			Run struct {
				ID           ids.ID
				CurrentStage string
			}
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return err
		}
		fmt.Printf("run %s started on change %s at stage %q\n", out.Run.ID, *changeID, out.Run.CurrentStage)
		fmt.Printf("next: apptown follow %s\n", *changeID)
		return nil
	}
	if *project == "" || *workflow == "" || *title == "" {
		return fmt.Errorf("-p, -w and -t are required")
	}
	// Inside a console session, invoke goes through the session's scoped
	// actuator endpoint (the read-only port rejects the general one).
	path := "/projects/" + *project + "/changes"
	if sid := os.Getenv("APPTOWN_SESSION_ID"); sid != "" && os.Getenv("APPTOWN_SESSION_TOKEN") != "" {
		path = "/console/" + sid + "/invoke"
	}
	raw, err := api.request("POST", path,
		map[string]string{"title": *title, "type": *typ, "body": *body, "workflow": *workflow, "intent": *intent})
	if err != nil {
		return err
	}
	var out struct {
		Change struct {
			ID     ids.ID
			Number int64
		}
		Run struct {
			ID           ids.ID
			CurrentStage string
		}
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	name := changeName(*project, out.Change.Number, out.Change.ID)
	fmt.Printf("change %s opened; run %s started at stage %q\n", name, out.Run.ID, out.Run.CurrentStage)
	if out.URL != "" {
		fmt.Printf("link: %s\n", out.URL)
	}
	fmt.Printf("next: apptown follow %s\n", name)
	return nil
}
