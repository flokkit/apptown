package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/project"

	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/strs"
)

type ApprovalBriefs struct {
	work       *workflow.Service
	selection  modelSelection
	completion modelcall.Completer
	db         *database.DB
	dataDir    string
}

func NewApprovalBriefs(work *workflow.Service, selection modelSelection, completion modelcall.Completer, db *database.DB, dataDir string) ApprovalBriefs {
	return ApprovalBriefs{work: work, selection: selection, completion: completion, db: db, dataDir: dataDir}
}
func (h ApprovalBriefs) ApprovalBrief(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	inputs, err := h.work.ApprovalBrief(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	if inputs.Cached != "" {
		WriteJSON(w, 200, ApprovalBrief{Brief: inputs.Cached})
		return
	}
	brief, err := h.buildApprovalBrief(r.Context(), inputs, h.diffStat(r.Context(), inputs))
	if err != nil {
		WriteFault(w, err)
		return
	}
	if brief != "" {
		if _, err := workflow.AddArtifactIn(context.WithoutCancel(r.Context()), h.db, inputs.Run.ID, "approval-brief", fmt.Sprintf("step-%d", id), "", brief, h.work.Now()); err != nil {
			log.Printf("background write failed: %v — artifact %s %q on run %d", err, "approval-brief", fmt.Sprintf("step-%d", id), inputs.Run.ID)
		}
	}
	WriteJSON(w, 200, ApprovalBrief{Brief: brief})
}

// ApprovalQuestion: GET /api/v1/steps/{id}/question — what approving the
// step agrees to, for a client to show before it sends the decision.
func (h ApprovalBriefs) ApprovalQuestion(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	question, err := h.work.ApprovalQuestion(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, question)
}

// diffStat is the brief's account of the change's committed diff, base to
// branch: its files and changed lines, that it changes nothing, or that it
// could not be read. It is never left out silently: the approver accepts
// the diff, and "could not be read" is not "no changes".
func (h ApprovalBriefs) diffStat(ctx context.Context, inputs workflow.ApprovalBriefInputs) string {
	ch := inputs.Change
	if ch == nil {
		return ""
	}
	if ch.Branch == "" {
		return "the change has no branch yet"
	}
	source, err := workflow.ReadSourceIn(ctx, h.db, workflow.RunRef{ID: inputs.Run.ID, ProjectID: inputs.Run.ProjectID})
	if err != nil {
		return "could not be read: " + err.Error()
	}
	repo, err := changeRepository(ctx, h.dataDir, project.RepositoryRef{ID: source.ProjectID, Slug: source.Slug}, ch.ID)
	if err != nil {
		return "could not be read: " + err.Error()
	}
	hub, err := source.Repository(h.dataDir)
	if err != nil {
		return "could not be read: " + err.Error()
	}
	comparison, err := scm.ReadChangeDiff(ctx, hub, repo, source.DefaultBranch, ch.BaseSHA, ch.Branch)
	if err != nil {
		return "could not be read: " + err.Error()
	}
	if comparison.Empty {
		return "the branch changes nothing"
	}
	files, err := comparison.Files(ctx, repo)
	if err != nil {
		return "could not be read: " + err.Error()
	}
	return files
}

// buildApprovalBrief writes the brief from its inputs and the diff's stat
// (diffStat). It fails only when the fast model's selection cannot be read.
func (h ApprovalBriefs) buildApprovalBrief(ctx context.Context, inputs workflow.ApprovalBriefInputs, diff string) (string, error) {
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
		ev := decodeBriefMap(string(s.Evidence))
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

	cfg := decodeBriefMap(string(step.Config))
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
	pattern, _, _, err := h.selection.Resolve(ctx, run.ProjectID, "fast")
	if err != nil {
		return "", err
	}
	if pattern == "" || h.completion == nil {
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
	out, _, err := h.completion.Ask(ctx, modelcall.Call{Purpose: "approval-brief", ProjectID: run.ProjectID, RunID: run.ID, Model: pattern, System: system, User: skeleton})
	if err != nil || strings.TrimSpace(out) == "" {
		return skeleton, nil
	}
	return strings.TrimSpace(out), nil
}

func decodeBriefMap(raw string) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &m)
	return m
}

// ApprovalBrief is the plain-words brief shown to an approver.
type ApprovalBrief struct {
	Brief string `json:"brief"`
}
