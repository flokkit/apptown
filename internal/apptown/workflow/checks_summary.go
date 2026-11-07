package workflow

import (
	"context"
	"encoding/json"
	"fmt"
)

// ChecksSummary is Workflow's display verdict. Readers never infer a pass from
// the step's execution status: a completed step can contain failed checks.
type ChecksSummary struct {
	Tone    string `json:"tone"`
	Text    string `json:"text"`
	Started bool   `json:"started"`
}

func isCheck(kind string) bool {
	switch kind {
	case "tests", "scan", "script", "guidance/conformance", "agent/review", "scm/merged":
		return true
	}
	return false
}

func (s Step) checkAttention() bool {
	if !isCheck(s.Type) || s.Status == StatusNotApplicable {
		return false
	}
	if s.OverrideBy != "" || s.Status != StatusPass {
		return true
	}
	r := s.CheckReading
	if r == nil && carriesCheckEvidence(s.Type) {
		r = readingOf(s.Evidence)
	}
	if r != nil {
		if r.Unreadable != "" || r.CheckErrors > 0 || len(r.Undecided) > 0 {
			return true
		}
		for _, c := range r.Checks {
			if c.Verdict != VerdictPass && c.Verdict != VerdictNotApplicable && c.Verdict != VerdictSkipped && (c.Gates == nil || *c.Gates) {
				return true
			}
		}
	}
	if s.Type == "tests" {
		var ev struct {
			TestCounts *struct {
				Skipped int `json:"skipped"`
			} `json:"testCounts"`
			TestReportUnreadable string `json:"testReportUnreadable"`
		}
		if json.Unmarshal(s.Evidence, &ev) != nil || ev.TestCounts == nil || ev.TestCounts.Skipped > 0 || ev.TestReportUnreadable != "" {
			return true
		}
	}
	return false
}

func summarizeChecks(steps []*Step) ChecksSummary {
	out := ChecksSummary{Tone: "neutral", Text: "Not checked yet. Checks run when you press Check and land in the chat."}
	failed, notes := 0, 0
	var failedTitle string
	unknown, running := false, false
	for _, s := range steps {
		if !isCheck(s.Type) {
			continue
		}
		if s.Status != StatusPending && s.Status != StatusSkipped {
			out.Started = true
		}
		r := s.CheckReading
		if r == nil && carriesCheckEvidence(s.Type) {
			r = readingOf(s.Evidence)
		}
		blockingFailure := false
		if r != nil {
			for _, c := range r.Checks {
				if c.Gates != nil && !*c.Gates {
					if c.Verdict != VerdictPass && c.Verdict != VerdictNotApplicable && c.Verdict != VerdictSkipped {
						notes++
					}
				} else if c.Verdict == VerdictFail {
					blockingFailure = true
				}
			}
		}
		if !s.checkAttention() {
			continue
		}
		switch {
		case s.OverrideBy != "":
			unknown = true
		case s.Status == StatusFail || s.Status == StatusError || s.Status == StatusNeedsHuman || blockingFailure:
			failed++
			failedTitle = checkTitle(workflowActivityStep{kind: s.Type, name: s.Name, evidence: s.Evidence})
		case s.Status == StatusPending || s.Status == StatusRunning:
			if s.runEnded {
				unknown = true
			} else {
				running = true
			}
		default:
			unknown = true
		}
	}
	if !out.Started {
		return out
	}
	switch {
	case failed > 0:
		out.Tone = "bad"
		out.Text = fmt.Sprintf("%d checks failed", failed)
		if failed == 1 {
			out.Text = failedTitle + " failed"
		}
	case unknown:
		out.Tone, out.Text = "unknown", "Checks not all known"
	case running:
		out.Text = "Checks running"
	default:
		out.Tone, out.Text = "ok", "All checks passed"
		if notes > 0 {
			noun := "notes"
			if notes == 1 {
				noun = "note"
			}
			out.Text += fmt.Sprintf(" · %d %s", notes, noun)
		}
	}
	return out
}

// ChangeChecks includes the working run carried by the current landing. The
// selection and verdict belong here so every change view uses the same facts.
func (s *Service) ChangeChecks(ctx context.Context, latest *RunSnapshot) (ChecksSummary, error) {
	if latest == nil {
		return summarizeChecks(nil), nil
	}
	steps := append([]*Step(nil), latest.Steps...)
	if latest.Definition.Role == "landing" {
		history, err := s.ChangeRuns(ctx, latest.Run.ChangeID, ChangeRuns{RootsOnly: true})
		if err != nil {
			return ChecksSummary{}, err
		}
		for _, run := range history {
			if run.ID.String() >= latest.Run.ID.String() {
				continue
			}
			spec, err := run.Definition()
			if err != nil {
				return ChecksSummary{}, err
			}
			if spec.Role != "working" {
				continue
			}
			work, err := s.Snapshot(ctx, run.ID, DetailView)
			if err != nil {
				return ChecksSummary{}, err
			}
			steps = append(work.Steps, steps...)
			break
		}
	}
	return summarizeChecks(steps), nil
}
