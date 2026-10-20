package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/strs"
)

// The coding loop: intent verbs (fix, quick, plan, build, uplift, adopt,
// retire) that open a Change on the right lane and STAY WITH IT — streaming
// step transitions and agent activity, showing the report at each approval
// gate, taking the approve/reject decision inline, offering the landing, and
// finishing at "shipped · release #N · main URL". One command, one loop,
// eyes on one terminal.

type verbSpec struct {
	workflow  string
	titleTmpl string // %s = first line of the body
	needsBody bool
}

var verbs = map[string]verbSpec{
	"fix":    {"fix-bug", "fix: %s", true},
	"quick":  {"quick-change", "%s", true},
	"plan":   {"plan-execute", "%s", true},
	"build":  {"implement", "%s", true},
	"uplift": {"dependency-uplift", "dependency uplift", false},
	// excavation: body is optional focus/context ("whole system" default)
	"excavate": {"excavate", "excavate: %s", false},
}

func cmdWorkVerb(verb string, args []string) error {
	spec := verbs[verb]
	fs := newFlags(verb)
	project := fs.String("p", "", "project slug (default: APPTOWN_PROJECT or cwd)")
	yes := fs.Bool("y", false, "auto-approve gates and landing (recorded as you)")
	noFollow := fs.Bool("no-follow", false, "start the run and return")
	title := fs.String("t", "", "change title (default: derived from the task)")
	asJSON := fs.Bool("json", false, "print what was opened as JSON and return (no following, no questions)")
	body := strings.TrimSpace(strings.Join(parseArgs(fs, args), " "))
	if spec.needsBody && body == "" {
		return fmt.Errorf(`usage: apptown %s [-p project] "<describe the %s>"`, verb, verb)
	}
	if err := confirmCommandWord(verb, body, !*asJSON); err != nil {
		return err
	}
	slug, err := resolveProject(*project)
	if err != nil {
		return err
	}
	t := *title
	if t == "" {
		first := body
		if i := strings.IndexByte(first, '\n'); i > 0 {
			first = first[:i]
		}
		first = strs.Truncate(first, 72)
		if first == "" {
			first = strings.TrimSuffix(spec.titleTmpl, ": %s") // bodyless verbs keep a clean title
		} else if strings.Contains(spec.titleTmpl, "%s") {
			first = fmt.Sprintf(spec.titleTmpl, first)
		}
		t = first
	}
	if *asJSON {
		opened, err := openChange(slug, spec.workflow, t, body)
		if err != nil {
			return err
		}
		return printJSON(opened)
	}
	return openAndFollow(slug, spec.workflow, t, body, *yes, *noFollow)
}

// confirmCommandWord asks before opening a change whose whole request is
// one word that names a forge command: `apptown quick -p shop follow` opened
// a change titled "follow" (sweep 7). Without a terminal it refuses and
// says what to write instead.
func confirmCommandWord(verb, body string, mayAsk bool) error {
	if strings.ContainsAny(body, " \t\n") || !commandNamed(body) {
		return nil
	}
	if !isTTY || !mayAsk {
		return fmt.Errorf("%q is an AppTown command: for that, run apptown %s …; to ask for a change, describe it in more than one word", body, body)
	}
	if ans := ask(fmt.Sprintf("%q is also an AppTown command (apptown %s). open a %s change asking just %q? [y/N] ", body, body, verb, body)); !strings.EqualFold(ans, "y") {
		return fmt.Errorf("nothing opened; for the command, run apptown %s …", body)
	}
	return nil
}

// openChange asks the forge to open a change and start its workflow, and
// says what it opened.
func openChange(slug, workflow, title, body string) (Opened, error) {
	raw, err := api.request("POST", "/projects/"+slug+"/changes",
		map[string]string{"title": title, "body": body, "workflow": workflow})
	if err != nil {
		return Opened{}, err
	}
	var out struct {
		Change struct {
			ID     ids.ID
			Number int64
		}
		Run struct {
			ID     ids.ID
			Number int64
		}
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Opened{}, err
	}
	return Opened{Change: changeRefOf(slug, out.Change.Number, out.Change.ID),
		Run: RunRef{ID: out.Run.ID, Number: out.Run.Number}, Workflow: workflow}, nil
}

func openAndFollow(slug, workflow, title, body string, yes, noFollow bool) error {
	opened, err := openChange(slug, workflow, title, body)
	if err != nil {
		return err
	}
	name := opened.Change.Name
	fmt.Printf("%s change %s on %s · %s · run %s\n",
		bold("▶"), bold(name), bold(slug), cyan(workflow), opened.Run.ID)
	if noFollow {
		fmt.Printf("  follow: apptown follow %s\n", name)
		return nil
	}
	return followChange(opened.Change.ID, opened.Run.ID, slug, yes)
}

// changeRuns is the part of a change's page the loop reads: its state, its
// project, its root runs with their roles, and what the forge offers on the
// change itself (land, send back).
type changeRuns struct {
	Change struct {
		ID     ids.ID
		Number int64
		State  string
	}
	Next        []nextAct
	ProjectSlug string
	Runs        []struct {
		ID     ids.ID
		Status string
		Role   string
	}
}

// landingAfter is the newest landing run started after run runID: the one
// accepting that run's work started, whether it is still going or ended.
func landingAfter(v changeRuns, runID ids.ID) (id ids.ID, status string, ok bool) {
	for _, r := range v.Runs {
		if r.Role == "landing" && r.ID.Compare(runID) > 0 && r.ID.Compare(id) > 0 { // ids order by creation
			id, status, ok = r.ID, r.Status, true
		}
	}
	return id, status, ok
}

// isLanding says whether run runID of the change is a landing.
func isLanding(v changeRuns, runID ids.ID) bool {
	for _, r := range v.Runs {
		if r.ID == runID {
			return r.Role == "landing"
		}
	}
	return false
}

// landingQueued says whether the forge has queued a landing for run runID's
// accepted work that has not started yet: the forge's stated fact.
func landingQueued(runID ids.ID) bool {
	var d struct{ LandingQueued bool }
	return api.get(fmt.Sprintf("/runs/%d", runID), &d) == nil && d.LandingQueued
}

// followChange drives a Change's current run to completion, then offers the
// landing and drives that too. Returns when shipped, parked, or failed.
func followChange(changeID, runID ids.ID, slug string, yes bool) error {
	status, err := streamRunFor(runID, changeID, yes)
	if err != nil {
		return err
	}
	var cv changeRuns
	if err := api.get(fmt.Sprintf("/changes/%d", changeID), &cv); err != nil {
		return err
	}
	if slug == "" {
		slug = cv.ProjectSlug
	}
	name := changeName(slug, cv.Change.Number, changeID)
	if isLanding(cv, runID) {
		if status == "done" {
			printShipped(name, slug, runID)
		} else {
			printLandingStopped(name, status)
		}
		return nil
	}
	if status != "done" {
		fmt.Printf("%s run %s ended %s — apptown show %s for the record\n", yellow("⏸"), runID, status, name)
		printDispute(runID)
		fmt.Printf("%s\n", dim(fmt.Sprintf(`  next:
    apptown retry %s              another round: fresh agent, feedback applies
    apptown retry %s -c           same agent continues its session (keeps its memory)
    apptown retry %s --restart    start over: wind the work back and go again
    apptown sendback %s "<note>"  rework from your note: say what to change
    apptown override %s "<why>"   overrule the failed check (your reason is recorded)
    apptown abandon %s ["<why>"]  close the change (its record is kept)`,
			name, name, name, name, name, name)))
		return nil
	}
	// Accepting the work starts the landing on the forge (the acceptance
	// handoff). Follow that landing, or say how it ended, before ever
	// starting one: a landing the acceptance started and catch-up ended
	// once had the CLI start a second one 3 s later (sweep 7, C3).
	for waited := 0; ; waited++ {
		if id, lstatus, ok := landingAfter(cv, runID); ok {
			if lstatus == "running" {
				fmt.Printf("%s landing · run %s %s\n", bold("▶"), id, dim("(started by the acceptance)"))
				return followChange(changeID, id, slug, yes)
			}
			if lstatus == "done" {
				printShipped(name, slug, id)
			} else {
				printLandingStopped(name, lstatus)
			}
			return nil
		}
		if cv.Change.State == "shipped" {
			printShipped(name, slug, ids.Nil)
			return nil
		}
		// A landing queued for this work starts on its own; wait for it.
		if waited >= 40 || !landingQueued(runID) {
			break
		}
		pause(pollEvery)
		if err := api.get(fmt.Sprintf("/changes/%d", changeID), &cv); err != nil {
			return err
		}
	}
	// Working run done → offer the landing, when the forge offers one.
	if _, ok := actOf(cv.Next, "land"); !ok {
		fmt.Printf("%s AppTown offers no landing for change %s now — apptown show %s\n", yellow("⏸"), name, name)
		return nil
	}
	ans := "y"
	if !yes {
		ans = ask(fmt.Sprintf("%s work accepted. land change %s now (gates → merge → deploy)? [y/N] ", green("✓"), name))
	}
	if !strings.EqualFold(ans, "y") {
		fmt.Printf("  later: apptown land %s\n", name)
		return nil
	}
	// Freshen the narrative first: the landing approval reviews the story,
	// so the story must include the run that just finished (the async
	// distill loses this race under -y).
	_, _ = api.request("POST", fmt.Sprintf("/changes/%d/distill", changeID), map[string]string{})
	raw, err := landWhenClear(changeID, os.Stdout)
	if err != nil {
		return err
	}
	var lr struct {
		Run struct{ ID ids.ID }
	}
	if err := json.Unmarshal(raw, &lr); err != nil {
		return err
	}
	fmt.Printf("%s landing · run %d\n", bold("▶"), lr.Run.ID)
	return followChange(changeID, lr.Run.ID, slug, yes)
}

// shippedDeployment is the part of a deployment the shipped line names.
type shippedDeployment struct {
	Name, URL, Observation string
	ObservationStale       bool
	ObservedAt             string // "" until the forge's first look
	ReleaseRun             ids.ID
}

// printShipped ends the loop: the release, and the deployment the landing
// fed with its observed state. The address is printed whatever was observed
// (it once appeared only when the app was observed running, sweep 7, A2),
// and the observation says what the forge last saw there.
func printShipped(name, slug string, landingRun ids.ID) {
	type release struct {
		Number int
		State  string
	}
	relLine := ""
	var relList []release
	var relObj struct{ Releases []release }
	if api.get("/projects/"+slug+"/releases", &relList) == nil && len(relList) > 0 {
		relLine = fmt.Sprintf(" · release #%d", relList[0].Number)
	} else if api.get("/projects/"+slug+"/releases", &relObj) == nil && len(relObj.Releases) > 0 {
		relLine = fmt.Sprintf(" · release #%d", relObj.Releases[0].Number)
	}
	var deps []shippedDeployment
	_ = api.get("/projects/"+slug+"/deployments", &deps)
	fmt.Printf("%s change %s shipped%s%s\n", green("⚑"), name, relLine, deploymentsLine(deps, landingRun))
}

// deploymentsLine names the deployments the landing released to (all the
// project's deployments when none names that run), each with its address
// and what the forge last observed there.
func deploymentsLine(deps []shippedDeployment, landingRun ids.ID) string {
	var fed []shippedDeployment
	for _, d := range deps {
		if !landingRun.IsZero() && d.ReleaseRun == landingRun {
			fed = append(fed, d)
		}
	}
	if len(fed) == 0 {
		fed = deps
	}
	line := ""
	for _, d := range fed {
		if d.Name == "" {
			continue
		}
		// Right after a landing the forge has not looked yet: that is not
		// an unknown state, and it reads as the web's "awaiting
		// observation" does.
		seen := "its state is not known"
		switch {
		case d.ObservedAt == "":
			seen = "not observed yet"
		case d.Observation == "running", d.Observation == "stopped":
			seen = d.Observation
			if d.ObservationStale {
				seen += " when last observed"
			}
		}
		line += strings.TrimRight(fmt.Sprintf(" · %s %s", d.Name, d.URL), " ") + " " + dim("("+seen+")")
	}
	return line
}

// loopRunDetail mirrors the run-detail payload, including artifacts and the
// forge's own reading of what the run is doing (State, StateDetail: the
// words the change page shows).
type loopRunDetail struct {
	Run struct {
		ID           ids.ID
		Status       string
		CurrentStage string
		WorkflowName string
	}
	State       string
	StateDetail string
	// Next is what the forge offers a person on the run now, each act
	// with its step.
	Next   []nextAct
	Stages []string
	Steps  []struct {
		ID         ids.ID
		Stage      string
		Name       string
		Type       string
		Status     string
		Config     map[string]any
		Evidence   map[string]any
		StartedAt  string
		FinishedAt string
	}
	Artifacts []struct {
		ID   ids.ID
		Kind string
		Name string
	}
}

// pollEvery is how long the loop waits between reads of a run.
var pollEvery = 1500 * time.Millisecond

// loopMove is the loop's next move on a running run, read from the forge's
// own state and acts rather than worked out from step statuses: "approve"
// (the forge offers the approval the run waits on; step and name say which),
// "waiting" (a person's decision the loop cannot take here, such as one in a
// called workflow; name is the forge's words), "blocked" (the stage cannot
// pass on its own; name is the forge's words), or "wait". The forge knows
// when a failed verdict is about to be checked again after a rework; the
// loop once called that blocked after a few polls (sweep 7, B1).
func loopMove(d loopRunDetail) (move string, step ids.ID, name string) {
	switch d.State {
	case "waiting on you":
		if a, ok := actOf(d.Next, "approve"); ok {
			return "approve", a.StepID, a.Step
		}
		return "waiting", ids.Nil, d.StateDetail
	case "blocked":
		return "blocked", ids.Nil, d.StateDetail
	}
	return "wait", ids.Nil, ""
}

// stepTook is how long a step ran by the forge's own clock, zero when the
// forge has not recorded both ends. A loop re-attached to a long step once
// timed it from the moment it attached ("✓ apply-change 26s" for a
// many-minute step, sweep 7).
func stepTook(startedAt, finishedAt string) time.Duration {
	start, err1 := time.Parse(time.RFC3339, startedAt)
	end, err2 := time.Parse(time.RFC3339, finishedAt)
	if err1 != nil || err2 != nil || end.Before(start) {
		return 0
	}
	return end.Sub(start)
}

// heartbeatEvery is how long the loop stays silent before saying what the
// run is still doing.
var heartbeatEvery = 30 * time.Second

// heartbeatLine says what a run that has printed nothing for a while is
// doing: each running step and how long it has run by the forge's clock,
// and the forge's own words when they say more (a queue, a slot). An image
// scan or deploy of several minutes once showed only "⧗ deploy" (sweep 7).
func heartbeatLine(d loopRunDetail, now time.Time) string {
	var parts []string
	names := map[string]bool{}
	for _, s := range d.Steps {
		if s.Status != "running" {
			continue
		}
		names[s.Name] = true
		part := s.Name + " still running"
		if t, err := time.Parse(time.RFC3339, s.StartedAt); err == nil && now.After(t) {
			part += " · " + fmtMs(now.Sub(t).Milliseconds())
		}
		parts = append(parts, part)
	}
	if d.StateDetail != "" && !names[d.StateDetail] {
		parts = append(parts, d.State+": "+d.StateDetail)
	}
	if len(parts) == 0 {
		return ""
	}
	return "  " + dim("… "+strings.Join(parts, "; "))
}

// streamRunFor renders change changeID's run live and handles its approval
// gates inline.
func streamRunFor(runID, changeID ids.ID, yes bool) (string, error) {
	lastStatus := map[ids.ID]string{}
	liveFrom := map[ids.ID]int{}
	started := map[ids.ID]time.Time{}
	shownStage := ""
	shownArtifacts := map[ids.ID]bool{}
	waitingSaid := ""
	quietSince := now()
	// say prints a line of the run's story; a heartbeat comes only after
	// heartbeatEvery without one.
	say := func(format string, a ...any) {
		fmt.Printf(format, a...)
		quietSince = now()
	}
	for {
		var d loopRunDetail
		if err := api.get(fmt.Sprintf("/runs/%d", runID), &d); err != nil {
			return "", err
		}
		if d.Run.CurrentStage != shownStage {
			shownStage = d.Run.CurrentStage
			say("%s\n", bold("── "+shownStage+" ──"))
		}
		for _, s := range d.Steps {
			if lastStatus[s.ID] != s.Status {
				lastStatus[s.ID] = s.Status
				switch s.Status {
				case "running":
					started[s.ID] = now()
					say("  %s %s %s\n", cyan("⧗"), s.Name, dim("("+s.Type+")"))
				case "pass":
					el := ""
					if took := stepTook(s.StartedAt, s.FinishedAt); took > 0 {
						el = dim(" " + fmtMs(took.Milliseconds()))
					} else if t, ok := started[s.ID]; ok {
						el = dim(fmt.Sprintf(" %ds", int(now().Sub(t).Seconds())))
					}
					say("  %s %s%s\n", green("✓"), s.Name, el)
				case "fail", "error":
					reason := ""
					if r, ok := s.Evidence["reason"].(string); ok {
						reason = dim(" — " + truncateStr(r, 160))
					}
					if v, ok := s.Evidence["verdict"].(string); ok && reason == "" {
						reason = dim(" — verdict: " + v)
					}
					say("  %s %s%s\n", red("✗"), s.Name, reason)
				case "needs_human":
					say("  %s %s %s\n", yellow("⚠"), s.Name, dim("needs a human"))
				}
			}
			if s.Status == "running" && (s.Type == "agent/pi" || s.Type == "agent/review") {
				var live struct {
					Lines []string
					Total int
				}
				if api.get(fmt.Sprintf("/steps/%d/live?from=%d", s.ID, liveFrom[s.ID]), &live) == nil {
					for _, l := range live.Lines {
						say("    %s\n", dim(truncateStr(l, 200)))
					}
					liveFrom[s.ID] = live.Total
				}
			}
		}
		if d.Run.Status != "running" {
			return d.Run.Status, nil
		}
		switch move, stepID, name := loopMove(d); move {
		case "approve":
			if err := handleApproval(&d, changeID, stepID, name, yes, shownArtifacts); err != nil {
				return "", err
			}
			quietSince = now()
		case "waiting":
			if waitingSaid != name {
				waitingSaid = name
				say("%s waiting on you: %s — apptown status %d\n", yellow("⏸"), name, runID)
			}
		case "blocked":
			say("%s blocked at %s by: %s — apptown status %d\n", yellow("⏸"), d.Run.CurrentStage, name, runID)
			return "blocked", nil
		default:
			if now().Sub(quietSince) >= heartbeatEvery {
				if line := heartbeatLine(d, now()); line != "" {
					say("%s\n", line)
				}
			}
		}
		pause(pollEvery)
	}
}

// handleApproval shows the approval's briefing once, then takes the
// decision inline. -y approves without prompting — same identity, same
// receipt, just fewer keystrokes — and says in one line what it agreed to
// instead of the briefing's paragraphs, which -y has chosen not to read
// (sweep 7: three paragraphs on every approval under -y).
func handleApproval(d *loopRunDetail, changeID, stepID ids.ID, stepName string, yes bool, shown map[ids.ID]bool) error {
	var q approvalQuestion
	if err := api.get(fmt.Sprintf("/steps/%d/question", stepID), &q); err != nil {
		return err
	}
	// What was shown of the landing goes back with the approval.
	approve := map[string]any{"decision": "approve", "alsoApproves": q.LandingMeans}
	if d := q.addsDigest(); d != "" {
		approve["adds"] = d
	}
	if yes {
		fmt.Println(questionLine(stepName, q))
		// Under -y the briefing is skipped, but not what the commit adds:
		// it is what the approval accepts into the app.
		printAdds(os.Stdout, q.Adds, "  ")
		if _, err := api.request("POST", fmt.Sprintf("/steps/%d/approve", stepID), approve); err != nil {
			// The step may have been decided while we looked: an acceptance
			// that also approves the landing carries over to it. A step that
			// passed meanwhile is not an error; following continues. Any
			// other refusal is the forge's answer.
			if decided, ok := stepDecided(d.Run.ID, stepID); ok && decided {
				fmt.Printf("  %s %s %s\n", green("✓"), stepName, dim("already decided"))
				return nil
			}
			return err
		}
		return nil
	}
	// The landing ceremony approves the NARRATIVE: show the Change's
	// distilled "So far" at approve-landing, not a build artifact.
	if !changeID.IsZero() && strings.Contains(stepName, "landing") && !shown[changeID] {
		var view struct {
			Change struct{ Summary string }
		}
		if api.get(fmt.Sprintf("/changes/%d", changeID), &view) == nil && strings.TrimSpace(view.Change.Summary) != "" {
			fmt.Printf("%s\n%s\n%s\n", dim("┄┄ so far ┄┄"), indentBody(view.Change.Summary, 30), dim("┄┄"))
			shown[changeID] = true
		}
	}
	// The BRIEF is the gate's front page: what this is, what machines
	// verified, what your approval decides ("I don't really understand
	// what I'm meant to be approving" was the complaint). Raw reports remain
	// available via apptown show.
	// Records' ids never collide, so the change's summary, the step's brief
	// and each artifact are marked shown in one map.
	if !shown[stepID] {
		var bv struct{ Brief string }
		if api.get(fmt.Sprintf("/steps/%d/brief", stepID), &bv) == nil && strings.TrimSpace(bv.Brief) != "" {
			fmt.Printf("%s\n%s\n%s\n", dim("┄┄ briefing ┄┄"), indentBody(bv.Brief, 76), dim("┄┄"))
			shown[stepID] = true
		}
	}
	// Fall back to the latest report artifact if no brief came back.
	if !shown[stepID] {
		for i := len(d.Artifacts) - 1; i >= 0; i-- {
			a := d.Artifacts[i]
			if shown[a.ID] {
				continue
			}
			if (strings.HasSuffix(a.Kind, "report") || a.Kind == "plan") &&
				!strings.Contains(a.Kind, "scan") && !strings.Contains(a.Kind, "sbom") {
				var art struct{ Body string }
				if api.get(fmt.Sprintf("/artifacts/%d", a.ID), &art) == nil && art.Body != "" {
					fmt.Printf("%s\n%s\n%s\n", dim("┄┄ "+a.Kind+" ┄┄"), indentBody(art.Body, 76), dim("┄┄"))
				}
				shown[a.ID] = true
				break
			}
		}
	}
	printQuestion(os.Stdout, q)
	if !isTTY {
		return fmt.Errorf("%s", noTerminalAdvice(changeID, stepID, stepName))
	}
	docs := stageDocuments(*d)
	showDocs := ""
	if len(docs) > 0 {
		showDocs = "[s]how " + strings.Join(docs, " & ") + " / "
	}
	for {
		switch ans := ask(fmt.Sprintf("  %s [a]pprove / [r]eject / %s[d]iff / [q]uit-leave-pending: ", bold(stepName), showDocs)); strings.ToLower(ans) {
		case "s", "show":
			printDocuments(*d, docs)
		case "a", "y", "approve":
			_, err := api.request("POST", fmt.Sprintf("/steps/%d/approve", stepID), approve)
			return err
		case "r", "reject":
			comment := ask("  why (sent to the agent as feedback): ")
			_, err := api.request("POST", fmt.Sprintf("/steps/%d/approve", stepID),
				map[string]string{"decision": "reject", "comment": comment})
			if err != nil {
				return err
			}
			// The forge gives the stage's agent your words on a rejection;
			// following shows the rework.
			fmt.Println(dim("  rejected; your words are recorded"))
			return nil
		case "d", "diff":
			raw, err := api.request("GET", fmt.Sprintf("/runs/%d/diff", d.Run.ID), nil)
			if err == nil {
				fmt.Println(truncateStr(string(raw), 8000))
			}
		case "q", "quit":
			return fmt.Errorf("left pending; resume with apptown follow <change-id> or apptown approve %d", stepID)
		}
	}
}

// stageDocuments are the kinds of document the approval's stage requires
// (its artifact/require steps): the plan, spec or report the approval is
// about. The approval's instructions send people to them ("Read the plan",
// "Skim 'Needs a human' first"), and the loop once offered only the diff.
func stageDocuments(d loopRunDetail) []string {
	var kinds []string
	for _, s := range d.Steps {
		if kind, _ := s.Config["kind"].(string); s.Stage == d.Run.CurrentStage && s.Type == "artifact/require" && kind != "" {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

// printDocuments prints the newest document of each kind in full.
func printDocuments(d loopRunDetail, kinds []string) {
	for _, kind := range kinds {
		var id ids.ID
		for _, a := range d.Artifacts {
			if a.Kind == kind && a.ID.Compare(id) > 0 {
				id = a.ID
			}
		}
		var art struct{ Body string }
		if id.IsZero() || api.get(fmt.Sprintf("/artifacts/%d", id), &art) != nil {
			fmt.Printf("  %s\n", dim("no "+kind+" could be read"))
			continue
		}
		fmt.Printf("%s\n%s\n%s\n", dim("┄┄ "+kind+" ┄┄"), indentBody(art.Body, 400), dim("┄┄"))
	}
}

// noTerminalAdvice is what the loop says when an approval waits and there is
// no terminal to ask at: which change and step, the exact commands that
// decide it, and the page. It once said only "approve in the UI, or rerun
// with -y" (sweep 7).
func noTerminalAdvice(changeID, stepID ids.ID, stepName string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "approval %q (step %d) is waiting on you, and this is not a terminal to ask at.\n", stepName, stepID)
	fmt.Fprintf(&b, "  approve it:          apptown approve %d -y   (apptown reject %d -m \"why\" sends it back)\n", stepID, stepID)
	if !changeID.IsZero() {
		fmt.Fprintf(&b, "  approve and go on:   apptown follow %d -y\n", changeID)
		fmt.Fprintf(&b, "  or on its page:      %s/changes/%d", api.base, changeID)
	}
	return strings.TrimRight(b.String(), "\n")
}

// cmdFollow re-attaches the loop to a Change's active (or latest) run.
func cmdFollow(args []string) error {
	fs := newFlags("follow")
	yes := fs.Bool("y", false, "auto-approve gates and landing")
	args = parseArgs(fs, args)
	if len(args) < 1 {
		return fmt.Errorf("usage: apptown follow <change-id> [-y]")
	}
	id, err := changeRef(args[0], "")
	if err != nil {
		return err
	}
	var view struct {
		Change struct {
			ID        ids.ID
			ProjectID ids.ID
			State     string
		}
		ProjectSlug string
		Runs        []struct {
			ID     ids.ID
			Status string
		}
	}
	if err := api.get(fmt.Sprintf("/changes/%d", id), &view); err != nil {
		return err
	}
	if len(view.Runs) == 0 {
		return fmt.Errorf("change %s has no runs yet — apptown invoke -c %s -w <workflow>", id, id)
	}
	latest := view.Runs[0]
	for _, r := range view.Runs {
		if r.ID.Compare(latest.ID) > 0 {
			latest = r
		}
	}
	return followChange(view.Change.ID, latest.ID, view.ProjectSlug, *yes)
}

// stepDecided re-reads run runID and says whether step stepID is no longer
// waiting for a decision; ok is false when the run cannot be read.
func stepDecided(runID, stepID ids.ID) (decided, ok bool) {
	var fresh loopRunDetail
	if api.get(fmt.Sprintf("/runs/%d", runID), &fresh) != nil {
		return false, false
	}
	return stepNoLongerAsking(fresh, stepID)
}

// stepNoLongerAsking says whether approval step stepID of d has been decided,
// on positive evidence: its own recorded status is pass (an acceptance that
// carried over, another vote, an override). That the forge no longer offers
// it is no evidence: a rework or unfinished work also takes the offer away.
// found is false for a step the run does not have.
func stepNoLongerAsking(d loopRunDetail, stepID ids.ID) (decided, found bool) {
	for _, s := range d.Steps {
		if s.ID == stepID {
			return s.Status == "pass", true
		}
	}
	return false, false
}
