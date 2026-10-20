package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// bootScreenshots is a boot step's evidence of AppTown's pictures of its
// preview (workflowexec.screenshots).
type bootScreenshots struct {
	Commit      string                        `json:"commit"`
	Shots       []struct{ Path, View string } `json:"shots"`
	Unavailable string                        `json:"unavailable"`
}

// latestScreenshots is the newest finished boot step's screenshots among a
// run's steps: this run's only, never an earlier one's.
func latestScreenshots(steps []cockpitStep) *bootScreenshots {
	for i := len(steps) - 1; i >= 0; i-- {
		s := steps[i]
		if s.Type != "preview" || s.Status == "running" {
			continue
		}
		var ev struct {
			Screenshots *bootScreenshots `json:"screenshots"`
		}
		if json.Unmarshal(s.Evidence, &ev) == nil && ev.Screenshots != nil {
			return ev.Screenshots
		}
	}
	return nil
}

// cockpitStep is a step of the change page's latest run, as show reads it.
type cockpitStep struct {
	Type     string          `json:"type"`
	Status   string          `json:"status"`
	Evidence json.RawMessage `json:"evidence"`
}

// screenshotsLine says what a person approving can see of the app: how
// many pictures AppTown took and from which commit, compared with the
// change's current commit (head), and where to look at them.
func screenshotsLine(s *bootScreenshots, head, link string) string {
	if s == nil {
		return ""
	}
	if s.Unavailable != "" && len(s.Shots) == 0 {
		return "screenshots could not be taken: " + s.Unavailable
	}
	short := func(c string) string {
		if len(c) > 7 {
			return c[:7]
		}
		return c
	}
	cmp := "not compared with the change's current commit"
	switch {
	case s.Commit != "" && head != "" && s.Commit == head:
		cmp = "the change's current commit"
	case s.Commit != "" && head != "":
		cmp = fmt.Sprintf("a different commit: the change is now at %s", short(head))
	}
	pages := make([]string, 0, len(s.Shots))
	for _, sh := range s.Shots {
		pages = append(pages, sh.Path+" "+sh.View)
	}
	return fmt.Sprintf("screenshots: %d taken from %s (%s): %s — %s", len(s.Shots), short(s.Commit), cmp, strings.Join(pages, ", "), link)
}
