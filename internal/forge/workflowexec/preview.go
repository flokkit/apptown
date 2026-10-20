package workflowexec

import (
	"context"
	"errors"
	"log"
	"strings"

	"flokkit.io/forge/internal/forge/preview"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"

	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/forge/workflow"
)

// Previews: a change or a run materialised on an ephemeral host port from
// its workspace, torn down when it ends; the reapers that keep the host
// clean.

// RunPreview executes a preview step: start the app from the run workspace on
// an ephemeral port; the URL becomes an artifact (the gate's materialization).
func (a *Shipping) RunPreview(task *workflow.Execution) {
	step := task.Step()
	run := task.Run()
	var cfg struct {
		Services []deploy.ServiceSpec `json:"services"`
	}
	cfgErr := workflow.DecodeStepConfig(step.Config, &cfg)
	finish := func(status workflow.StepState, evidence map[string]any) {
		task.Settle(workflow.EncodeOutcome(status, evidence, nil))
	}
	if _, err := task.Claim(); err != nil {
		return
	}
	if cfgErr != nil {
		finish(workflow.StatusError, map[string]any{"reason": cfgErr.Error()})
		return
	}

	source, err := workflow.ReadSourceIn(task.Context(), a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	// The commit the run's gates read (verification.SelectCommit): the
	// folder's HEAD, or the run's branch when the folder is behind work
	// published there.
	sel, err := verification.SelectCommit(task.Context(), a.db, a.cfg.DataDir, run, source)
	if err != nil {
		finish(workflow.StatusError, map[string]any{"reason": "the run's commit to preview: " + err.Error()})
		return
	}
	name := docker.PreviewRunName(run.ID)
	// The one sequence (boot.go), from the run's commit: whether there is
	// an app to boot, and whether its contract reads, are decided from the
	// same commit that boots. From-source cold boots legitimately take tens
	// of seconds (npm install + bundler + compile) — the budget must cover
	// an honest build, not a dial. The stack lives as long as the preview
	// container: the run cleanup command tears both down.
	booted, err := a.previews.RunPreview(task.Context(), task, preview.RunInput{Source: deploy.Source{Workspace: sel.Repo, Revision: sel.Commit, Slug: a.projectSlugOf(task.Context(), run.ProjectID)}, Services: cfg.Services})
	if errors.Is(err, docker.ErrNoContract) {
		// Nothing to boot is a fact, not a failure — but say so in the
		// evidence rather than manufacturing a hollow "app is up".
		finish(workflow.StatusPass, map[string]any{"skipped": "no .apptown/app.yaml in the run's commit — project declares no runtime contract, boot gate vacuous"})
		return
	}
	var unreadable *preview.ContractError
	if errors.As(err, &unreadable) {
		task.Settle(workflow.EncodeRework(map[string]any{"reason": err.Error()}))
		return
	}
	res := booted.Boot
	url := booted.URL
	if err != nil && !preview.IsAppBootFailure(err) {
		// Where previews run could not be asked, or cannot run them: the
		// forge's execution failed, and the app is not judged.
		finish(workflow.StatusError, map[string]any{"reason": err.Error()})
		return
	}
	if err != nil {
		be := docker.AsBootError(err)
		switch be.Phase {
		case docker.PhaseNeeds:
			finish(workflow.StatusError, map[string]any{"reason": be.Error()})
		case docker.PhaseSecrets:
			// A person's task (bind the secret), or a declaration the agent
			// must fix: either way a fail with the sentence, never an error.
			finish(workflow.StatusFail, map[string]any{"reason": be.Error()})
		case docker.PhaseProbe, docker.PhasePage:
			// A page a visitor is refused, or a static site whose page is
			// missing, fails like an app that never answered: the why
			// rides as the log tail, ahead of the app's own last words.
			logTail := be.LogTail
			if be.Phase == docker.PhasePage {
				logTail = strings.TrimSpace(be.Error() + "\n" + be.LogTail)
			}
			ev := map[string]any{"reason": "app did not come up in a cold container", "url": url, "logTail": logTail}
			if res.Page != nil {
				ev["frontPage"] = res.Page
			}
			task.Settle(workflow.EncodeRework(ev))
		default:
			finish(workflow.StatusFail, map[string]any{"reason": be.Error()})
		}
		return
	}
	public := res.Stack.Plan.PublicPart()
	status := workflow.StatusPass
	recordArtifact(task.Context(), a.db, run.ID, "preview", "preview", url, "", a.now())
	recordEvent(task.Context(), a.db, run.ID, "preview.started", map[string]any{"url": url, "container": name}, a.now())

	ev := map[string]any{"url": url, "container": name, "port": public.Port, "command": public.Command, "commit": sel.Commit}
	// What a person approving sees of the app: AppTown's own pictures of
	// this preview, from this commit. Failing to take them is said, and
	// the boot still passed.
	ev["screenshots"] = a.screenshots(task.Context(), run, url, sel.Commit)
	if booted.Incarnation != "" {
		ev["incarnation"] = booted.Incarnation
	}
	if res.Page != nil {
		ev["frontPage"] = res.Page
	}
	if len(res.Stack.Secrets.Receipts) > 0 {
		ev["secrets"] = res.Stack.Secrets.Receipts
		ev["secretProfile"] = res.Stack.Secrets.Profile
	}
	if public.Image != "" {
		// The digest names the exact bytes that just passed — the receipt a
		// deploy can be compared against — read where the preview runs.
		ev["image"] = public.Image
		ev["digest"] = booted.Digest
	}
	finish(status, ev)
}

// screenshots takes and records AppTown's pictures of a run's preview at
// url, booted from commit: the home page, and the pages the run's report
// names on its `Pages:` line. Each picture is an artifact of the run; the
// answer is the boot's evidence of them.
func (a *Shipping) screenshots(ctx context.Context, run *workflow.Run, url, commit string) map[string]any {
	ev := map[string]any{"commit": commit}
	if a.shots == nil {
		ev["unavailable"] = "this AppTown takes no screenshots"
		return ev
	}
	// A browser in a container opens the preview at its own address, as a
	// visitor does: a preview reached only at a port of AppTown's host
	// has no such address.
	if !a.previews.PreviewsHaveDomain() {
		ev["unavailable"] = "previews here have no address of their own (no preview domain), so AppTown cannot open them as a visitor would"
		return ev
	}
	var pages, ignored []string
	claim, err := workflow.ReadClaimIn(ctx, a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID}, run.ChangeID)
	if err != nil {
		log.Printf("run %s: reading the report's pages to picture: %v", run.ID, err)
	} else if claim != nil {
		pages, ignored = verification.NamedPages(claim.Body)
	}
	if len(ignored) > 0 {
		ev["ignored"] = ignored
	}
	c := a.shots.Take(ctx, url, pages)
	if len(c.Missing) > 0 {
		ev["missing"] = c.Missing
	}
	if c.Unavailable != "" {
		ev["unavailable"] = c.Unavailable
		return ev
	}
	shots := []map[string]any{}
	for _, s := range c.Shots {
		id, err := workflow.AddArtifactIn(context.WithoutCancel(ctx), a.db, run.ID, ScreenshotKind, s.Path+" · "+s.View, url, string(s.PNG), a.now())
		if err != nil {
			ev["missing"] = append(c.Missing, s.Path+" · "+s.View+": not recorded: "+err.Error())
			continue
		}
		shots = append(shots, map[string]any{"artifact": id, "path": s.Path, "view": s.View})
	}
	if len(shots) == 0 {
		ev["unavailable"] = "no picture could be recorded"
		return ev
	}
	ev["shots"] = shots
	return ev
}

// ScreenshotKind is the artifact kind of AppTown's pictures of a preview.
const ScreenshotKind = "screenshot"
