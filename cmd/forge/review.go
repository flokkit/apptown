package main

import (
	"fmt"
	"sort"
	"strings"

	"flokkit.io/forge/internal/ids"
)

// cmdReview starts a conformance review — every project, or the one -p
// names, checked against the ways of working it adopted — and follows it
// to its findings, as the Conformance page's "Review now" does. There was
// no way to start one from a terminal (sweep 7).
func cmdReview(args []string) error {
	fs := newFlags("review")
	project := fs.String("p", "", "review this project only (default: every project)")
	noFollow := fs.Bool("no-follow", false, "start the review and return")
	parseArgs(fs, args)
	body := map[string]any{"build": true}
	scope := "every project"
	if *project != "" {
		body["projects"] = []string{*project}
		scope = *project
	}
	var rev struct {
		ID     ids.ID
		Number int64
	}
	if err := api.post("/conformance/review", body, &rev); err != nil {
		return err
	}
	link := fmt.Sprintf("%s/conformance/reviews/%s", api.base, rev.ID)
	fmt.Printf("%s conformance review %d of %s %s\n", bold("▶"), rev.Number, scope, dim(link))
	if *noFollow {
		return nil
	}
	said := ""
	for {
		var d reviewDetail
		if err := api.get(fmt.Sprintf("/conformance/reviews/%d", rev.ID), &d); err != nil {
			return err
		}
		switch d.Review.Status {
		case "queued":
			if line := "  … queued behind another review or what-if"; line != said {
				said = line
				fmt.Println(dim(line))
			}
		case "running":
			if line := fmt.Sprintf("  … %d of %d projects reviewed", d.Review.ProjectsDone, d.Review.ProjectsTotal); line != said {
				said = line
				if d.Review.CurrentProject != "" {
					line += " · now " + d.Review.CurrentProject
				}
				fmt.Println(dim(line))
			}
		case "done":
			fmt.Print(reviewSummary(d))
			fmt.Printf("  the review: %s\n", link)
			return nil
		default:
			return fmt.Errorf("conformance review %d ended %s — %s", rev.Number, d.Review.Status, link)
		}
		pause(2 * pollEvery)
	}
}

// reviewDetail is a review and its verdicts (GET /conformance/reviews/{id}).
type reviewDetail struct {
	Review struct {
		Status                      string
		ProjectsDone, ProjectsTotal int
		CurrentProject              string
	}
	Verdicts []struct {
		ProjectSlug, Check, State, Severity, Detail string
	}
}

// reviewSummary counts a finished review's verdicts by outcome and lists
// what does not conform, project by project.
func reviewSummary(d reviewDetail) string {
	counts := map[string]int{}
	var failing []string
	for _, v := range d.Verdicts {
		counts[v.State]++
		if v.State == "does-not-conform" {
			line := fmt.Sprintf("    %s · %s", v.ProjectSlug, v.Check)
			if v.Severity != "" {
				line += " (" + v.Severity + ")"
			}
			if v.Detail != "" {
				line += dim(" — " + truncateStr(v.Detail, 120))
			}
			failing = append(failing, line)
		}
	}
	var b strings.Builder
	states := make([]string, 0, len(counts))
	for s := range counts {
		states = append(states, s)
	}
	sort.Strings(states)
	parts := make([]string, 0, len(states))
	for _, s := range states {
		parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
	}
	if len(parts) == 0 {
		parts = append(parts, "no verdicts")
	}
	fmt.Fprintf(&b, "%s reviewed %d projects: %s\n", green("✓"), d.Review.ProjectsTotal, strings.Join(parts, ", "))
	if len(failing) > 0 {
		b.WriteString("  does not conform:\n" + strings.Join(failing, "\n") + "\n")
	}
	return b.String()
}
