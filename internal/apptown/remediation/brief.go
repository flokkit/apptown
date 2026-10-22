package remediation

import (
	"context"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
)

// Brief is AppTown-generated acceptance criterion an agent works
// from: what conformance looks like (the check's own words), what specifically
// failed here, and the three conditions landing must satisfy (§5.2). No invented
// definition of done — the check IS the definition.
func Brief(block string, version int, sc conformance.DeclaredCheck, findingDetail string) string {
	b := fmt.Sprintf(`Bring this app into conformance with %s@%d · %s.

WHAT CONFORMANCE LOOKS LIKE (the check's own expectation, this is the spec):
  %s
%s
WHAT FAILED HERE (from the fleet review):
  %s

Fix the WHOLE of what the check asks, not only the sentence the review
quoted: the review names the first thing it saw, the check judges everything
it lists, and a change that fixes one item bounces on the next.

ACCEPTANCE — all three must hold, and AppTown re-checks them on landing:
  1. the check %q re-evaluates to CONFORMS against your change's tree;
  2. the app's ordinary gates still pass (tests, boot/preview, review) — do not
     satisfy the check by deleting a feature or weakening the app;
  3. you did NOT edit the check definition itself — close the gap, never the check.
  4. you fixed the FAULT the check names, never the evidence of it: do not move,
     rename or delete a file to make the check stop seeing it. A source file
     that a rule catches means the rule is wrong; fix the rule. Output that a
     rule catches means the file is wrong; untrack it. Look at what the file
     IS before choosing. If the project does what the check forbids
     deliberately, or the fix would make another adopted check fail, say so in
     your report and stop — an exception is recorded, not a workaround.

SCOPE — change ONLY what fixing the stated issue needs. Nothing else, however
small or well-meant: no tidy-ups, no renames, no version bumps, no .gitignore
lines, no refactors beside the fix. If something else MUST change for the fix
to work, change it and explain it in your report under a heading "Also changed,
and why" — one line per file, what and why — so the reviewer can judge it. A
change that is neither needed for the fix nor explained there fails review.
Other findings that already fail on main are marked "preexisting" by apptown
check and the gate; they are not yours to fix here. Dependencies are not yours
to move: install exactly what the lockfile says; never run an update, bump a
version or remove a package (the gate refuses a remediation that did). You MAY
add a package the fix itself needs — add it to the manifest (and lockfile) and
name it under "Also changed, and why". If the pinned set cannot be installed,
say so in your report and stop. Never commit build
output (target/, dist/, build/, node_modules/, coverage/…) even when the
repository already tracks it: running the build or the tests may rewrite it;
restore it exactly as it was on the base before you commit.

If the repository has no application to bring into conformance — no source,
only a README or a seeded starter — say so in your report and stop. Do not
invent an app to satisfy a check; that is a build, which is another lane's
work and a person's decision (office-poll change 244, 2026-09-07).

Work in this Change's workspace and land through the ordinary gates.`,
		block, version, sc.Check.Name, sc.Check.Expects, howJudged(sc.Check), orDash(findingDetail), sc.Check.Name)
	return b
}

// howJudged is the check's own judging text for the brief: the rubric of a
// model-judged check, the script of a shell check. Without it the agent
// fixed the sentence the review quoted and missed the rest of the check,
// costing a round each time.
func howJudged(c conformance.CheckDeclaration) string {
	switch {
	case c.Rubric != "":
		return "\nHOW IT IS JUDGED (the check's rubric, every item of it):\n  " + c.Rubric + "\n"
	case c.Sh != "":
		return "\nHOW IT IS JUDGED (the check's own script):\n  " + c.Sh + "\n"
	}
	return ""
}

func orDash(s string) string {
	if s == "" {
		return "(no detail recorded)"
	}
	return s
}

// Unprovable says why no change can be accepted on a check, or "" when the
// landing gate judges it. A check answered only by a standing deployment's
// steady use (memory-within) is skipped at every landing, so a change whose
// acceptance it was could never land: its finding is fixed by an ordinary
// change the finding describes, then a deploy, or by an operator's override
// on the deployment, and is not remediation work.
func Unprovable(sc conformance.DeclaredCheck) string {
	if !check.JudgedAtLanding(sc.Check) {
		return fmt.Sprintf("%s · %s is judged only on the running deployment, so no change can prove it before it lands: make the change its finding describes and deploy, or override the deployment's resources on its card", sc.Block, sc.Check.Name)
	}
	return ""
}

// The title is presentation. Acceptance names the target independently, so
// editing a title can never change which finding this work has to close.
func AcceptanceCheck(sc conformance.DeclaredCheck) workflow.AcceptanceCheck {
	return workflow.AcceptanceCheck{Block: sc.Block, Version: sc.Version, Check: sc.Check.Name,
		Title: sc.Check.Title, Expects: sc.Check.Expects}
}

func ChangeTitle(sc conformance.DeclaredCheck) string {
	if title := strings.TrimSpace(sc.Check.Title); title != "" {
		return title
	}
	return fmt.Sprintf("Conform to %s@%d · %s", sc.Block, sc.Version, sc.Check.Name)
}

// remediationLane picks the lane a remediation Change is worked on: the
// friction-floor quick-change lane when the project offers it — one agent
// step whose prompt carries the request and its scope, a boot, an
// independent review, one acceptance — else implement. The implement lane's
// self-verify step, pinned at older library versions on most projects,
// "fixes small defects" and widened README fixes into contracts and
// endpoints; a one-check remediation is a small change and is
// worked as one.
func WorkflowFor(ctx context.Context, resolver WorkflowResolver, p *project.Project) string {
	for _, lane := range []string{"quick-change", "implement"} {
		if _, err := resolver.SelectedWorkflow(ctx, p, lane); err == nil {
			return lane
		}
	}
	return ""
}

type WorkflowResolver interface {
	SelectedWorkflow(ctx context.Context, p *project.Project, requested string) (*adoption.ResolvedWorkflow, error)
}
