package briefs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/workflow"

	"github.com/flokkit/apptown/internal/strs"
)

// buildApprovalBrief writes the brief from its inputs and committed diff stat.
// Unreadable evidence, instructions or model selection
// fail the build without caching partial prose.
func (s *Service) buildApprovalBrief(ctx context.Context, inputs workflow.ApprovalBriefInputs, diff string) (string, error) {
	step, run := inputs.Step, inputs.Run
	// ---- mechanical skeleton: always true, always available ----
	var sk strings.Builder

	var title, body, summary string
	if inputs.Change != nil {
		title, body, summary = inputs.Change.Title, inputs.Change.Body, inputs.Change.Summary
	}
	sk.WriteString("THE ASK: " + title + "\n")
	if b := strings.TrimSpace(body); b != "" {
		sk.WriteString(strs.Truncate(b, 700) + "\n")
	}
	// What a person asked since is part of the ask: without it the brief
	// called the edits a person had requested deviations (sweep 7, B2).
	if a := inputs.Asked; a != nil && strings.TrimSpace(a.Note) != "" {
		sk.WriteString("\nWHAT WAS ASKED SINCE (a person sent the work back; this is part of the ask):\n" +
			strs.Truncate(strings.TrimSpace(a.Note), 700) + " (— " + a.By + ")\n")
	}
	if s := strings.TrimSpace(summary); s != "" {
		sk.WriteString("\nSTORY SO FAR:\n" + strs.Truncate(s, 900) + "\n")
	}

	sk.WriteString("\nMACHINES ALREADY VERIFIED:\n")
	for _, s := range inputs.Steps {
		if s.ID == step.ID || s.Type == "approval" {
			continue
		}
		ev, err := decodeBriefMap(s.Evidence)
		if err != nil {
			return "", fmt.Errorf("reading %s evidence: %w", s.Name, err)
		}
		line := "- " + s.Name + " (" + s.Type + "): " + s.Status.String()
		// An override is always shown as one, whatever else the step
		// carries: its status reads pass, and an overridden review's summary
		// alone would present a waived check as a passed one.
		if s.OverrideBy != "" {
			line = "- " + s.Name + " (" + s.Type + "): " + fmt.Sprintf("OVERRIDDEN by %s: %s", s.OverrideBy, strs.Truncate(s.OverrideReason, 200)) + " (the check itself did not pass)"
		}
		switch {
		case s.Type == "agent/review" && ev["summary"] != nil:
			line += " — " + strs.Truncate(fmt.Sprint(ev["summary"]), 220)
		case s.Type == "preview" && ev["url"] != nil:
			line += fmt.Sprintf(" — booted cold, served at %v", ev["url"])
			if ev["digest"] != nil {
				line += fmt.Sprintf(" (image %v)", ev["digest"])
			}
		case ev["skipped"] != nil:
			line += fmt.Sprintf(" — %v", ev["skipped"])
		}
		sk.WriteString(line + "\n")
	}

	if d := strings.TrimSpace(diff); d != "" {
		sk.WriteString("\nTHE DIFF (the change's whole branch against its base):\n" + strs.Truncate(d, 1500) + "\n")
	}

	report := inputs.Report
	if report != "" {
		sk.WriteString("\nIMPLEMENTER'S REPORT (a claim, already reviewed above):\n" +
			strs.Truncate(report, 1200) + "\n")
	}

	cfg, err := decodeBriefMap(step.Config)
	if err != nil {
		return "", fmt.Errorf("reading approval instructions: %w", err)
	}
	if means, _ := cfg["means"].(string); means != "" {
		sk.WriteString("\nWHAT YOUR OK MEANS HERE: " + means + "\n")
	}
	if instr, _ := cfg["instructions"].(string); instr != "" {
		sk.WriteString("\nGATE INSTRUCTIONS: " + instr + "\n")
	}
	skeleton := sk.String()

	// ---- fast-slot refinement into an actual briefing ----
	// The fast model's selection that cannot be read is a failure, not "no
	// model": nothing is saved as the brief.
	pattern, _, _, err := s.selection.Resolve(ctx, run.ProjectID, "fast")
	if err != nil {
		return "", err
	}
	if pattern == "" || s.completion == nil {
		return skeleton, nil
	}
	system := `You write APPROVAL BRIEFS for a software delivery gate. The reader is
the human approver, possibly seeing this work for the first time. From
the material given, write:

WHAT THIS IS — two plain sentences: what was asked and what was built.
  What was asked includes WHAT WAS ASKED SINCE, if present: work a
  person asked for when sending the work back is not a deviation.
ALREADY VERIFIED — one line per machine check that PASSED, plainly.
  Include overrides prominently: who overrode what and why.
YOUR CALL — the heart: what does approving actually decide? What is the
  one judgment machines could NOT make (taste, product intent, risk
  acceptance)? Be specific to THIS change, not generic.
WORTH A LOOK — anything odd: failing/overridden checks, deviations the
  report admits, scope beyond the ask (and what was asked since). Omit the section if nothing.

Plain language. No markdown headers other than the four labels above.
Under 220 words. Do not invent facts not in the material.`
	out, _, err := s.completion.Ask(ctx, modelcall.Call{Purpose: "approval-brief", ProjectID: run.ProjectID, RunID: run.ID, Model: pattern, System: system, User: skeleton})
	if err != nil || strings.TrimSpace(out) == "" {
		return skeleton, nil
	}
	return strings.TrimSpace(out), nil
}

func decodeBriefMap(raw json.RawMessage) (map[string]any, error) {
	m := map[string]any{}
	if len(raw) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}
