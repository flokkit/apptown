package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/strs"
)

// cmdWhatIf runs a what-if of a published pack version — the version's
// checks judged on each project's main beside what it adopted — and follows
// it to its answer, as the pack page's What-if does. Without -p it runs on
// every eligible project: one on an earlier version of the pack. A release
// publishes stock versions without recommending them, so this is how a
// person tries one from a terminal before `apptown pack recommend`. Model
// rubrics are judged only with --rubrics and a spend limit in dollars. It
// reports and does not judge: a finished what-if exits zero whatever it
// found; one that stopped or was interrupted does not.
func cmdWhatIf(args []string) error {
	if len(args) > 0 && args[0] == "adopt" {
		return cmdWhatIfAdopt(args[1:])
	}
	if len(args) > 0 && args[0] == "show" {
		pos := parseArgs(newFlags("whatif show"), args[1:])
		if len(pos) != 1 {
			return fmt.Errorf("usage: apptown whatif show <id>")
		}
		if err := idArg(pos, "what-if id"); err != nil {
			return err
		}
		var w whatIf
		if err := api.get("/conformance/what-if/"+pos[0], &w); err != nil {
			return err
		}
		return whatIfEnd(w)
	}
	fs := newFlags("whatif")
	projects := fs.String("p", "", "the projects to judge, comma-separated (default: every project on an earlier version)")
	noFollow := fs.Bool("no-follow", false, "start the what-if and return")
	rubrics := fs.String("rubrics", "", "include model rubrics, with this spend limit in dollars")
	pos := parseArgs(fs, args)
	usage := fmt.Errorf("usage: apptown whatif <pack> <version> [-p <project>[,<project>…]] [--rubrics <dollars>] [--no-follow]")
	if len(pos) != 2 {
		return usage
	}
	version, err := strconv.ParseInt(pos[1], 10, 64)
	if err != nil || version < 1 {
		return fmt.Errorf("the version is a positive number, not %q", pos[1])
	}
	var slugs []string
	for _, s := range strings.Split(*projects, ",") {
		if s = strings.TrimSpace(s); s != "" {
			slugs = append(slugs, s)
		}
	}
	body := map[string]any{"pack": pos[0], "version": version}
	limit := ""
	if *rubrics != "" {
		dollars, err := strconv.ParseFloat(strings.TrimPrefix(*rubrics, "$"), 64)
		micros := int64(dollars*1e6 + 0.5)
		if err != nil || micros <= 0 {
			return fmt.Errorf("--rubrics is a spend limit in dollars above zero, not %q", *rubrics)
		}
		body["rubrics"], body["spendLimitMicros"] = true, micros
		limit = money(micros)
	}
	scope := "every project on an earlier version"
	if len(slugs) > 0 {
		body["projects"] = slugs
		scope = strings.Join(slugs, ", ")
	}
	// Said before asking: once admitted, the what-if may spend.
	if limit != "" {
		fmt.Println(dim(fmt.Sprintf("model rubrics: one checklist per project, sent while spend is under %s; one project's calls may pass it", limit)))
	}
	var started struct{ ID ids.ID }
	if err := api.post("/conformance/what-if", body, &started); err != nil {
		return err
	}
	link := fmt.Sprintf("%s/conformance/what-ifs/%d", api.base, started.ID)
	fmt.Printf("%s what-if %s: %s v%d on %s %s\n", bold("▶"), started.ID, pos[0], version, scope, dim(link))
	if *noFollow {
		return nil
	}
	said := ""
	for {
		var w whatIf
		if err := api.get(fmt.Sprintf("/conformance/what-if/%d", started.ID), &w); err != nil {
			return err
		}
		line := ""
		switch w.State {
		case "queued":
			line = "  … queued behind another review or what-if"
		case "running":
			line = fmt.Sprintf("  … %d of %d projects judged", w.ProjectsDone, w.ProjectsTotal)
		default:
			if w.ID.IsZero() {
				w.ID = started.ID
			}
			return whatIfEnd(w)
		}
		if line != said {
			said = line
			fmt.Println(dim(line))
		}
		pause(2 * pollEvery)
	}
}

// whatIfEnd prints a what-if as read and is its exit: done is success
// whatever it found; stopped or interrupted is not an answer.
func whatIfEnd(w whatIf) error {
	switch w.State {
	case "done":
		fmt.Print(whatIfSummary(w))
	case "queued", "running":
		fmt.Print(whatIfSummary(w))
	default: // error, interrupted
		if len(w.Rows) > 0 || len(w.Projects) > 0 {
			fmt.Print(whatIfSummary(w))
		}
		msg := fmt.Sprintf("what-if %s ended %s", w.ID, w.State)
		if w.Detail != "" {
			msg += " — " + w.Detail
		}
		return fmt.Errorf("%s", msg)
	}
	fmt.Printf("  the what-if: %s/conformance/what-ifs/%d\n", api.base, w.ID)
	return nil
}

// whatIf is a what-if, its rows and its answer per project (GET
// /conformance/what-if/{id}).
type whatIf struct {
	ID                          ids.ID
	State                       string
	Detail                      string
	ProjectsDone, ProjectsTotal int
	Rubrics                     bool
	SpendLimitMicros            int64
	CostMicros                  int64
	Incomplete                  int64
	Rows                        []struct {
		ProjectSlug, Block, Check, Result, Detail, Reason string
	}
	Projects []struct {
		ProjectSlug, SHA, Outcome string
		Joins                     bool
		Fixed                     int
		NewFailures               []struct {
			Block, Check  string
			ExceptedToday bool
		}
		NotAssessed []struct{ Block, Check, Why string }
	}
}

var whatIfOutcomeWords = map[string]string{
	"new-failures": "new failures", "could-not-assess": "could not assess", "no-new-failures": "no new failures", "not-evaluated": "not evaluated", "not-yet-judged": "not yet judged",
}

// whatIfSummary counts the rows by result, then gives one line per project
// with its answer (new failures by check, what could not be assessed and
// why, fixes, the commit judged), then every row that is not unchanged,
// with the reason a row was not judged. A what-if that did not finish is
// labelled partial with how many projects it reached: the rest have no
// answer.
func whatIfSummary(w whatIf) string {
	var b strings.Builder
	counts := map[string]int{}
	var lines []string
	for _, r := range w.Rows {
		counts[r.Result]++
		if r.Result == "unchanged" {
			continue
		}
		line := fmt.Sprintf("    %s · %s · %s", r.Result, r.ProjectSlug, r.Check)
		if r.Block != "" {
			line += dim(" (" + r.Block + ")")
		}
		why := r.Detail
		if r.Result == "not-assessed" && r.Reason != "" {
			why = strings.TrimSpace(r.Reason + " " + r.Detail)
		}
		if why != "" {
			line += dim(" — " + truncateStr(why, 120))
		}
		lines = append(lines, line)
	}
	results := make([]string, 0, len(counts))
	for r := range counts {
		results = append(results, r)
	}
	sort.Strings(results)
	parts := make([]string, 0, len(results))
	for _, r := range results {
		parts = append(parts, fmt.Sprintf("%d %s", counts[r], r))
	}
	if len(parts) == 0 {
		parts = append(parts, "no rows")
	}
	sort.Strings(lines)
	// Queued and running are not ends: they say how far it has got, never
	// "partial" or "before it ended".
	switch w.State {
	case "done":
		fmt.Fprintf(&b, "%s judged %d projects: %s\n", green("✓"), w.ProjectsTotal, strings.Join(parts, ", "))
	case "queued":
		fmt.Fprintf(&b, "queued: waiting behind another review or what-if; no project judged yet\n")
	case "running":
		fmt.Fprintf(&b, "running: %d of %d projects judged so far: %s\n", w.ProjectsDone, w.ProjectsTotal, strings.Join(parts, ", "))
	default:
		fmt.Fprintf(&b, "partial: %d of %d projects judged before it ended: %s\n", w.ProjectsDone, w.ProjectsTotal, strings.Join(parts, ", "))
	}
	b.WriteString("  " + rubricSpend(w) + "\n")
	outcomes := map[string]int{}
	for _, p := range w.Projects {
		outcomes[p.Outcome]++
	}
	var answer []string
	for _, o := range []string{"new-failures", "could-not-assess", "no-new-failures", "not-evaluated", "not-yet-judged"} {
		if outcomes[o] > 0 {
			answer = append(answer, fmt.Sprintf("%d %s", outcomes[o], whatIfOutcomeWords[o]))
		}
	}
	if len(answer) > 0 {
		fmt.Fprintf(&b, "  by project: %s\n", strings.Join(answer, " · "))
	}
	for _, p := range w.Projects {
		var what []string
		for _, c := range p.NewFailures {
			s := c.Block + " · " + c.Check
			if c.ExceptedToday {
				s += " (excepted today)"
			}
			what = append(what, s)
		}
		for _, c := range p.NotAssessed {
			s := c.Block + " · " + c.Check
			if c.Block == "" {
				s = "the project"
			}
			what = append(what, s+": "+c.Why)
		}
		if p.Fixed > 0 {
			what = append(what, fmt.Sprintf("%d fixed", p.Fixed))
		}
		line := fmt.Sprintf("  %-18s %-16s", p.ProjectSlug, whatIfOutcomeWords[p.Outcome])
		if p.Joins {
			line += " (would join)"
		}
		if len(what) > 0 {
			line += " " + strings.Join(what, "; ")
		}
		if p.SHA != "" {
			line += dim(" @ " + strs.ShortSHA(p.SHA))
		}
		b.WriteString(line + "\n")
	}
	if len(lines) > 0 {
		b.WriteString(strings.Join(lines, "\n") + "\n")
	}
	return b.String()
}

// rubricSpend says whether model rubrics were included and what they cost
// against their limit: "at least" when some call's spend is unknown.
func rubricSpend(w whatIf) string {
	if !w.Rubrics {
		return "model rubrics not included"
	}
	spent := money(w.CostMicros)
	if w.Incomplete > 0 {
		// Incomplete counts checklists with some unknown spend, not calls: a
		// checklist's retry adds no count of its own.
		return fmt.Sprintf("model rubrics: spent at least %s of %s (some spend unknown)", spent, money(w.SpendLimitMicros))
	}
	return fmt.Sprintf("model rubrics: spent %s of %s", spent, money(w.SpendLimitMicros))
}

// money is micro-dollars as dollars and cents.
func money(micros int64) string { return fmt.Sprintf("$%.2f", float64(micros)/1e6) }

// cmdWhatIfAdopt opens one adoption Change per named project, taking what
// what-if id assessed for it, through each project's own adopt door in
// turn: each is its own authorized act and record, and the forge reads
// what was assessed from the what-if. It goes on after a refusal; it exits
// zero only when every project opened a Change. There is no "all": the
// projects are named.
func cmdWhatIfAdopt(args []string) error {
	fs := newFlags("whatif adopt")
	projects := fs.String("p", "", "the projects to adopt it on, comma-separated (required)")
	pos := parseArgs(fs, args)
	var slugs []string
	for _, s := range strings.Split(*projects, ",") {
		if s = strings.TrimSpace(s); s != "" {
			slugs = append(slugs, s)
		}
	}
	if len(pos) != 1 || len(slugs) == 0 {
		return fmt.Errorf("usage: apptown whatif adopt <id> -p <project>[,<project>…]")
	}
	id, err := parseID(pos[0], "what-if id")
	if err != nil {
		return err
	}
	opened := 0
	for _, slug := range slugs {
		var o struct{ ChangeID ids.ID }
		err := api.post("/projects/"+slug+"/adoption/adopt", map[string]any{"fromWhatIf": id, "viaChange": true}, &o)
		var refused *httpError
		switch {
		case err == nil:
			opened++
			fmt.Printf("  %-18s Change %s %s\n", slug, o.ChangeID, dim(fmt.Sprintf("%s/changes/%s", api.base, o.ChangeID)))
		case errors.As(err, &refused) && refused.Status < 500:
			fmt.Printf("  %-18s refused: %s\n", slug, refused.Error())
		default:
			fmt.Printf("  %-18s not known whether it opened; look at the project's Changes (%s)\n", slug, err.Error())
		}
	}
	if opened != len(slugs) {
		return fmt.Errorf("%d of %d projects opened an adoption Change", opened, len(slugs))
	}
	fmt.Printf("%s %d adoption Change%s opened, as assessed in what-if %s\n", green("✓"), opened, map[bool]string{true: "", false: "s"}[opened == 1], id)
	return nil
}
