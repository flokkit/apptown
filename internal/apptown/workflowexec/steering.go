package workflowexec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/database"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"

	"github.com/flokkit/apptown/internal/apptown/complaint"

	"github.com/flokkit/apptown/internal/apptown/agentenv"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy"
)

type steeringFile struct {
	Text           string
	Hash           string
	GuidanceHash   string
	AdoptionDigest string         // the revision the run read (§4.1)
	Skills         map[string]int // block name → version used
	Resources      map[string]int
}

// steeringPaths are where the agent finds what its steering names: the
// resource files, the change's attachments and its own transcripts, as the
// agent's container sees them.
type steeringPaths struct{ Resources, Attachments, Sessions string }

// folderSteering: an agent on AppTown's host finds them in the run's
// folder, under .apptown.
var folderSteering = steeringPaths{Resources: ".apptown/resources", Attachments: ".apptown/attachments", Sessions: ".apptown/sessions"}

// steeringResource is a resource file the steering names, at Path.
type steeringResource struct {
	Path string
	Body []byte
}

// buildSteering assembles the steering file (composeSteering) for an agent
// in the run's folder, stages resource files into the workspace, records
// the steering artifact, and writes .apptown/steering.md.
func (a *Executor) buildSteering(ctx context.Context, run *workflow.Run, step *workflow.Step, cfg *agentConfig, ws string, k agentKnobs) (steeringFile, error) {
	var attachments []string
	if !run.ChangeID.IsZero() {
		attachments = (workflow.GitWorkspace{DataDir: a.cfg.DataDir}).AttachmentNames(run.ChangeID)
	}
	// Delegation (pihome.go): only promised where it exists — the docker
	// executor's generated pi-home carries the baked extension; a local run
	// keeps ambient config off entirely.
	delegation := ""
	if k.executor == execution.Docker {
		delegation = a.home.Delegation(ctx)
	}
	st, resources, err := a.composeSteering(ctx, run, cfg, k, folderSteering, attachments, delegation)
	if err != nil {
		return st, err
	}
	for _, r := range resources {
		if err := os.WriteFile(filepath.Join(ws, filepath.FromSlash(r.Path)), r.Body, 0o644); err != nil {
			return st, err
		}
	}
	a.recordSteering(ctx, step, st)
	if err := os.WriteFile(filepath.Join(ws, ".apptown", "steering.md"), []byte(st.Text), 0o644); err != nil {
		return st, err
	}
	return st, nil
}

// recordSteering attaches the steering to the step's run. The steering file
// is a RECEIPT: the exact system-prompt text this step ran with (pinned
// guidance + generated registry section + skills + resources + services) is
// attached as a step artifact and hashed into evidence — dynamic sections
// included, nothing the model saw is uninspectable.
func (a *Executor) recordSteering(ctx context.Context, step *workflow.Step, st steeringFile) {
	ctx = context.WithoutCancel(ctx) // a record of the brief the agent was given
	if st.Text == "" {
		return
	}
	if res, err := workflow.AddArtifactIn(ctx, a.db, step.RunID, "steering", "steering-"+step.Name, "sha256:"+st.Hash, st.Text, a.now()); err == nil {
		id := res
		recordEvent(ctx, a.db, step.RunID, "artifact.added", map[string]any{"kind": "steering", "name": "steering-" + step.Name, "id": id, "hash": st.Hash}, a.now())
	}
}

// composeSteering assembles the steering text — compiled guidance +
// generated registry/integration sections + hygiene + attachments +
// delegation + runtime-contract + dind + skills + resources + services —
// and the resource files it names, as bytes. at says where the agent finds
// what the text names; attachments are the change's attachment names;
// delegation is the subagent section the agent's image supports ("" for
// none). Nothing is written.
func (a *Executor) composeSteering(ctx context.Context, run *workflow.Run, cfg *agentConfig, k agentKnobs, at steeringPaths, attachments []string, delegation string) (steeringFile, []steeringResource, error) {
	var steer strings.Builder
	steer.WriteString(complaint.AgentInstructions)
	receipt, err := adoption.ProjectAdoptionIn(ctx, a.db, run.ProjectID, run.AdoptionID)
	if _, err := database.Lookup(err); err != nil {
		// An unread adoption is not "none": steering without it would
		// leave the way of working out of the agent's brief.
		return steeringFile{}, nil, fmt.Errorf("reading the run's adoption: %w", err)
	}
	source := adoption.Source{ProjectID: run.ProjectID, Revision: receipt}
	resolved, gerr := a.adoption.Resolve(ctx, source)
	guidanceText, guidanceHash := resolved.Text, resolved.Hash
	if gerr != nil {
		return steeringFile{}, nil, fmt.Errorf("the run's guidance receipt could not be compiled: %w", gerr)
	}
	if guidanceText != "" {
		steer.WriteString("# Steering guidance (pinned by this project)\n\n" + guidanceText + "\n\n")
		// The mechanism inventory is INSTANCE state, generated from bindings —
		// never hand-written prose (design/subsystems/registries.md: norms in guidance,
		// WHERE generated).
		registries, err := a.steering.Registry(ctx)
		if err != nil {
			return steeringFile{}, nil, fmt.Errorf("the registries in the steering: %w", err)
		}
		if registries != "" {
			steer.WriteString(registries + "\n")
		}
		integrations, err := a.steering.Integrations(ctx, run.ProjectID)
		if err != nil {
			return steeringFile{}, nil, fmt.Errorf("the integrations in the steering: %w", err)
		}
		if integrations != "" {
			steer.WriteString(integrations + "\n")
		}
		// The sessions folder holds pi's own JSON transcripts — listing or
		// reading it dumps tens of thousands of tokens into context and
		// has killed runs (change 59's execute step died after one ls).
		steer.WriteString(agentenv.WorkspaceHygieneAt(at.Sessions, at.Attachments+"/"))
		if len(attachments) > 0 {
			steer.WriteString("# Attached reference material\n\nThe human attached files to this change; read them before working:\n")
			for _, n := range attachments {
				steer.WriteString("- " + at.Attachments + "/" + n + "\n")
			}
			steer.WriteString("\n")
		}
	}
	if delegation != "" {
		steer.WriteString(delegation + "\n")
	}
	// A change's step is given main by AppTown (main_merge.go), so its
	// agent is told not to catch up itself.
	if !run.ChangeID.IsZero() {
		if spec, err := run.Definition(); err == nil && spec.Role != "landing" {
			steer.WriteString(mainMergeBrief)
		}
	}
	// Runtime-contract topology: engine-owned facts about how apps deploy
	// (cold containers, mise pins, the boot gate). Injected for every agent
	// that might touch code or the contract — this knowledge living only in
	// some workflow prompts is exactly how reading-list shipped an unbootable
	// contract through a green pipeline.
	if hasSkill(cfg, "skill/implementation") {
		steer.WriteString(agentenv.RuntimeContractInstructions)
	}
	if k.useDind {
		steer.WriteString(agentenv.DockerInstructions)
	}
	st := steeringFile{GuidanceHash: guidanceHash, Skills: map[string]int{}, Resources: map[string]int{}}
	if receipt != nil {
		st.AdoptionDigest = receipt.Digest
	}
	for _, name := range cfg.Skills {
		body, ver, err := a.adoption.ResolveBlock(ctx, source, name)
		if err != nil {
			return st, nil, err
		}
		st.Skills[name] = ver
		steer.WriteString(fmt.Sprintf("# Skill: %s (v%d)\n\n%s\n\n", name, ver, strings.TrimSpace(body)))
	}
	var resources []steeringResource
	for _, name := range cfg.Resources {
		body, ver, err := a.adoption.ResolveBlock(ctx, source, name)
		if err != nil {
			return st, nil, err
		}
		st.Resources[name] = ver
		resources = append(resources, steeringResource{Path: at.Resources + "/" + strings.ReplaceAll(name, "/", "--") + ".md", Body: []byte(body)})
	}
	if len(resources) > 0 {
		steer.WriteString("# Resources\n\nThese files are available in the workspace:\n")
		for _, r := range resources {
			steer.WriteString("- " + r.Path + "\n")
		}
		steer.WriteString("\n")
	}
	if len(cfg.Services) > 0 {
		steer.WriteString("# Services\n\nThese sidecar services are running on your network, reachable by hostname (NOT localhost):\n")
		for _, svc := range cfg.Services {
			steer.WriteString(fmt.Sprintf("- **%s** (%s)", svc.Name, svc.Image))
			if len(svc.Env) > 0 {
				body, _ := json.Marshal(svc.Env)
				steer.WriteString(" env: " + string(body))
			}
			steer.WriteString("\n")
		}
		steer.WriteString("\nConfigure the application to use these (env vars or a local settings shim) — do not install servers yourself.\n")
	}
	st.Text = steer.String()
	st.Hash = deploy.ConfigHash(st.Text)
	return st, resources, nil
}

// agentPrompt is the message pi receives: the interpolated task, or on a
// retry-with-continue only what is new (the feedback). A restart retry has
// already wound the work back (restartFromBase).
func (a *Executor) agentPrompt(ctx context.Context, run *workflow.Run, cfg *agentConfig, source workflow.Source, earlier *wroteFile) string {
	var item *workflow.Change
	if !run.ChangeID.IsZero() {
		item, _ = workflow.ReadChangeIn(ctx, a.db, run.ChangeID)
	}
	projectSlug := source.Slug

	prompt := workflow.Interpolate(ctx, a.db, cfg.Prompt, run, item, projectSlug)
	switch cfg.RetryMode {
	case "continue":
		// Resuming the previous session: the agent already holds the task
		// and its own work in context — send only what's new. A human
		// send-back note (apptown retry -m) outranks gate feedback.
		fb := cfg.Sendback
		if fb == "" {
			fb = workflow.Interpolate(ctx, a.db, "${{feedback.latest}}", run, item, projectSlug)
		}
		prompt = "Your work was rejected. The feedback:\n\n" + fb +
			"\n\nContinue from where you left off in this session: address every point" +
			" precisely, update your report/output file to describe the WHOLE change," +
			" and finish."
	}
	if cfg.RetryMode != "continue" && cfg.Sendback != "" {
		prompt += "\n\nAdditional instructions for this attempt:\n\n" + cfg.Sendback
	}
	if cfg.needsRefused != "" {
		prompt += "\n\nAppTown refuses this app's declared needs in .apptown/app.yaml, so their" +
			" services (databases and the like) were not started for you: " + cfg.needsRefused +
			"\nUntil .apptown/app.yaml is fixed AppTown will not test, preview or release" +
			" the app, so fixing it is part of this change; say so in your report."
	}
	if earlier != nil {
		prompt += "\n\nYour report from the earlier attempt is in " + earlier.rel + ". UPDATE it, do not" +
			" replace it: it must still describe the WHOLE change, every file the" +
			" change touches, with what this attempt changed added and anything" +
			" no longer true corrected. A report of only this attempt's edits" +
			" leaves the rest of the change unclaimed."
	}
	return prompt
}

// restartFromBase is a restart retry's poisoned-approach reset: the run's work
// goes back to its base, in the workspace and on the branch, before a fresh
// attempt. The folder is reset first, confined (gitexec.Folder.Reset): a
// folder that cannot be reset fails the restart before the branch moves, so
// nothing can later republish the rewound commits from it. The branch then
// moves to the base by a compare-and-swap in the hub (nothing is fetched
// from the folder) under the new attempt's authority, so the
// replacement's commits build on what is published and an earlier attempt
// cannot publish again. A rewind that fails after the reset leaves the
// folder at the base and the branch where it was: the branch's tip is
// recorded for the folder (scm.MarkSaved), so the folder is behind and
// readers read the branch. Reflog keeps the old commits.
func (a *Executor) restartFromBase(ctx context.Context, run *workflow.Run, step *workflow.Step, source workflow.Source, folder *gitexec.Folder) error {
	baseSHA := source.RunBaseSHA
	if baseSHA == "" || !source.RepoBacked() {
		return nil
	}
	ws, repo := source.Workspace(a.cfg.DataDir), source.Repository(a.cfg.DataDir)
	if folder == nil || folder.Path() != filepath.Clean(ws) {
		return fmt.Errorf("restart: the run's folder %s is not locked", ws)
	}
	// The reset and the rewind are one restart: once the folder moves, the
	// branch follows though the caller stops.
	ctx = context.WithoutCancel(ctx)
	if err := folder.Reset(ctx, baseSHA, ".apptown"); err != nil {
		return fmt.Errorf("restart: resetting the workspace to %s: %w", baseSHA, err)
	}
	if _, err := a.branches.Publish(ctx, scm.Publication{ProjectID: run.ProjectID, Repository: repo,
		Branch: source.Branch, Commit: baseSHA, Rewind: true, Grant: scm.Grant{Generation: step.SourceGeneration()}}); err != nil {
		if tip := gitexec.HubRef(ctx, repo, "refs/heads/"+source.Branch); tip != "" && tip != baseSHA {
			if merr := scm.MarkSaved(ctx, ws, repo, source.Branch, tip); merr != nil {
				log.Printf("run %d: recording the branch's tip for the folder reset to its base: %v", run.ID, merr)
			}
		}
		return fmt.Errorf("restart: rewinding branch %s: %w", source.Branch, err)
	}
	return nil
}
