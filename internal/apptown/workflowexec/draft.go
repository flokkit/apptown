package workflowexec

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// Complete is one tool-free model call through the inference owner's
// admission (inference.Gateway.Complete), as the workload the token names.
type Complete func(ctx context.Context, token, model, system, prompt string) (string, error)

type Drafter struct {
	tokens    *identity.RuntimeTokens
	selection model.Selection
	complete  Complete
}

// NewDrafter creates the workflow drafting path. complete is nil on a
// AppTown with no inference gateway; drafting is then refused.
func NewDrafter(tokens *identity.RuntimeTokens, selection model.Selection, complete Complete) Drafter {
	return Drafter{tokens: tokens, selection: selection, complete: complete}
}

const assistReference = `You write workflow YAML for AppTown. Output ONLY a complete YAML document
(optionally in a single fenced code block) — no commentary before or after.

Document shape:
  apiVersion: apptown.dev/v1
  kind: Workflow
  metadata: { name: kebab-case, description: one line }
  spec:
    kind: lifecycle | pipeline    # lifecycle = human-paced stages; pipeline = auto-advance
    workspace: project            # optional; persistent project working tree (change-request style)
    on:                           # optional triggers; manual always works
      schedule: { cron: "0 6 1 * *" }
      webhook: {}
    defaults: { thinking: medium }  # optional; also model, executor, image
    stages:                       # ordered; a run advances only through gates
      - name: stage-name
        onEnter: [ ...steps auto-run on entry (agents/scripts)... ]
        steps:   [ ...checks evaluated for the gate... ]
        gate: { require: [step-names] }   # optional; default = all steps pass

Step types (name + uses + with):
- agent/pi: an AI agent works in the run workspace.
  with: prompt (required; multiline |), skills: [skill/...], resources: [resource/...],
        docker: true  # a private Docker daemon for the agent (building/running images)
        services: [{name, image, env: {K: V}}]  # sidecar containers, reachable by hostname
        output: {kind, name, file}  # file (e.g. REPORT.md): agent must WRITE the report there
        timeoutMinutes
  Prompt interpolation: ${{change.title}}, ${{change.body}}, ${{artifact.<kind>}},
  ${{latest.<kind>}} (newest artifact of kind in project), ${{feedback.latest}} (rejection comment).
  Convention: tell the agent to WRITE reports to the output file; chat is not captured.
- approval: human gate. with: approvers (names or group:<name>), quorum (OKs needed,
  default 1 — distinct people; one rejection halts), means (REQUIRED: one sentence
  stating what an OK asserts here, e.g. "I trust the agent did what it said" —
  an approval without a stated standard is rejected at save), instructions
- script: shell command, pass = exit 0. with: run, env, executor (local|docker), timeoutMinutes
- artifact/require: gate on an artifact existing. with: kind
- adr/require: gate on an accepted ADR linked to the work item. with: status (default accepted)
- scm/mr: push the run branch and open a GitLab MR (repo-backed projects with a remote).

Design taste: few stages (2-3), every agent stage followed by a human approval,
reports to files, ${{feedback.latest}} in agent prompts so rejection loops work,
effects last and always gated.`

// Draft asks the draft slot's model for a workflow and validates it,
// retrying once with the validation error fed back. Each attempt is its own
// admitted model call; no container or process starts.
//
// The call acts as the authenticated person behind by, and through a
// console only while that console lives: the inference owner checks both
// on each attempt and during it, as it does for a runtime's request.
// receipt is the request's attribution, kept for the receipt only; it
// grants nothing.
func (draft Drafter) Draft(ctx context.Context, about, prompt string, by access.Principal, receipt string) (string, int, error) {
	who, err := assistWorkload(by, receipt)
	if err != nil {
		return "", 0, err
	}
	steer := assistReference
	if about != "" {
		steer += "\n\nContext:\n" + about
	}
	token := draft.tokens.Mint(who)
	defer draft.tokens.Revoke(token)
	attempts := 0
	p := prompt
	var lastErr error
	for attempts < 2 {
		attempts++
		pattern, _, _ := draft.selection.Resolve(ctx, ids.Nil, "draft")
		call, cancel := context.WithTimeout(ctx, 4*time.Minute)
		text, err := draft.complete(call, token, pattern, steer, p)
		cancel()
		if err != nil {
			return "", attempts, fmt.Errorf("assist failed: %w", err)
		}
		yamlOut := extractYAML(text)
		if _, verr := schema.ParseWorkflow([]byte(yamlOut)); verr == nil {
			return yamlOut, attempts, nil
		} else {
			lastErr = verr
			p = prompt + "\n\nYour previous output failed validation with: " + verr.Error() +
				"\nOutput the corrected COMPLETE YAML only."
		}
	}
	return "", attempts, fmt.Errorf("draft failed validation after %d attempts: %w", attempts, lastErr)
}

// assistWorkload is the person a draft acts as, and the console it came
// through. A caller that is no person (anonymous, a run, AppTown itself)
// has no one to act as, so it cannot draft.
func assistWorkload(by access.Principal, receipt string) (identity.Workload, error) {
	who := identity.Workload{Kind: "assist", Actor: receipt}
	switch {
	case by.Kind == "user" && by.Name != "":
		who.Person, who.Principal = by.Name, by.ID
	case by.Kind == "instrument" && by.Instrument == "console" && by.Person != "" && by.ConsoleID != "":
		who.Person, who.Principal, who.SessionID = by.Person, by.ID, by.ConsoleID
	default:
		return identity.Workload{}, fault.New(fault.Forbidden, "drafting acts as a signed-in person; "+by.Name+" is not one")
	}
	if who.Actor == "" {
		who.Actor = who.Person
	}
	return who, nil
}

// ---- AI assist -------------------------------------------------------------

// extractYAML strips markdown fences and surrounding prose from model output.
func extractYAML(text string) string {
	t := strings.TrimSpace(text)
	if i := strings.Index(t, "```"); i >= 0 {
		rest := t[i:]
		lines := strings.SplitN(rest, "\n", 2)
		if len(lines) == 2 {
			body := lines[1]
			if j := strings.Index(body, "```"); j >= 0 {
				return strings.TrimSpace(body[:j])
			}
			return strings.TrimSpace(body)
		}
	}
	// No fences: take from the first plausible YAML start.
	if i := strings.Index(t, "apiVersion:"); i > 0 {
		return t[i:]
	}
	return t
}
