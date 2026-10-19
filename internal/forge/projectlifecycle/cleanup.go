package projectlifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/maintenance"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/usage"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitlab"
	"flokkit.io/forge/internal/strs"
)

// CleanupJob is the durable job that removes what a deleted project left
// outside the database. Its key is CleanupKey; a retry starts a new round.
const CleanupJob = "project-cleanup"

// CleanupAttempts is how many times one round tries before the cleanup is
// marked as needing a person. The queue's budget and the record's count agree.
const CleanupAttempts = 5

// CleanupKey names one cleanup round of a deleted project: project/<id>/<round>.
func CleanupKey(projectID ids.ID, round int) string {
	return fmt.Sprintf("project/%s/%d", projectID, round)
}

// CleanupSessions lists and removes the deleted project's console sessions.
type CleanupSessions interface {
	List(ctx context.Context, m console.Match) []*console.Session
	Remove(ctx context.Context, id string) error
	RemoveEnded(m console.Match) error
}

// WorkerPreviews removes everything of one preview for good: on the
// worker, when the forge has one, its stack and its build images; its look.
type WorkerPreviews interface {
	RemovePreviewForGood(ctx context.Context, name string) error
}

// GitLabClient opens the configured GitLab, read afresh for each attempt.
type GitLabClient interface {
	Client(ctx context.Context) (*gitlab.Client, error)
}

// Cleanup carries out a recorded cleanup plan. It never deletes records and
// never guesses at resources: it acts only on the identifiers the deletion
// fixed, every effect treats an already-absent resource as done, and the
// project's slug stays held until the cleanup is done, so a new project of
// the same name cannot be touched.
type Cleanup struct {
	Projects *project.Projects
	Sessions CleanupSessions
	Usage    *usage.Ledger
	Docker   *docker.Scope
	// Previews reaches previews on the worker, when the forge has them.
	Previews WorkerPreviews
	GitLab   GitLabClient
	DB       *database.DB
	DataDir  string
	// Now is the forge's clock, which stamps the cleanup's receipts and
	// settled attempts' receipts (the real clock when nil).
	Now func() time.Time
}

// Run is one attempt at the round named by key. A failed attempt stays
// pending for the queue to try again until CleanupAttempts is spent; the
// last failed attempt waits for a person. A round a later retry replaced is
// skipped.
func (c Cleanup) Run(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	var id ids.ID
	var round int
	parts := strings.Split(key, "/")
	if len(parts) != 3 || parts[0] != "project" {
		return nil, jobs.Permanent(fmt.Errorf("invalid cleanup key %q", key))
	}
	id, err := ids.Parse(parts[1])
	if err == nil {
		round, err = strconv.Atoi(parts[2])
	}
	if err != nil {
		return nil, jobs.Permanent(fmt.Errorf("invalid cleanup key %q", key))
	}
	d, err := c.Projects.Deletion(ctx, id)
	if err != nil {
		return nil, err
	}
	if d.State != project.CleanupPending || d.Round != round {
		return map[string]any{"skipped": d.State, "round": d.Round}, nil // a later round owns it
	}
	var plan CleanupPlan
	if err := json.Unmarshal(d.Plan, &plan); err != nil {
		return nil, jobs.Permanent(fmt.Errorf("reading the cleanup plan of project %d: %w", id, err))
	}
	failures := c.Clean(ctx, id, plan)
	state, detail := project.CleanupDone, ""
	if len(failures) > 0 {
		detail = strs.Truncate(strings.Join(failures, "; "), 2000)
		state = project.CleanupPending
		if d.Attempts+1 >= CleanupAttempts {
			state = project.CleanupNeedsAttention
		}
	}
	// What the cleanup did is settled though the job's context ended.
	if settled, err := c.Projects.SettleDeletionAttempt(context.WithoutCancel(ctx), id, round, state, detail); err != nil {
		return nil, err
	} else if !settled {
		return map[string]any{"skipped": "settled elsewhere"}, nil
	}
	if state == project.CleanupPending {
		return nil, fmt.Errorf("cleanup of %s: %s", plan.Slug, detail) // the queue tries again
	}
	// The outcome, done or waiting for a person, is the cleanup's receipt.
	audit.At(clock.Or(c.Now)()).BestEffort(ctx, c.DB, d.RequestedBy, ids.Nil, "project.cleanup", plan.Slug, map[string]any{"projectId": id, "state": state, "detail": detail, "round": round})
	return map[string]any{"state": state}, nil
}

// Clean applies every effect of the plan once and names each one that
// failed; it records nothing (Run settles the attempt).
func (c Cleanup) Clean(ctx context.Context, id ids.ID, plan CleanupPlan) []string {
	var failed []string
	note := func(err error) {
		if err != nil {
			failed = append(failed, err.Error())
		}
	}
	for _, s := range c.Sessions.List(ctx, conversation.ForProject(id)) {
		conversation.HarvestUsage(ctx, c.Usage, s)
		note(c.Sessions.Remove(ctx, s.ID))
	}
	// Ended chats are folders the registry no longer holds: their words and
	// pictures go with the project.
	note(c.Sessions.RemoveEnded(conversation.ForProject(id)))
	// With no local Docker nothing local was left (the mode's cutover
	// precondition), so its names are skipped, never failed on.
	local := !c.Docker.Absent()
	for _, run := range plan.RunIDs {
		note(c.Previews.RemovePreviewForGood(ctx, docker.PreviewRunName(run)))
		if local {
			note(c.Docker.RemoveDisposable(ctx, docker.PreviewRunName(run)))
			failed = append(failed, maintenance.RemoveBuildImages(ctx, *c.Docker, docker.PreviewRunName(run))...)
		}
		note(os.RemoveAll(filepath.Join(c.DataDir, "workspaces", fmt.Sprintf("run-%d", run))))
		// The snapshots the run's preview was served from, as for a change.
		note(os.RemoveAll(filepath.Join(c.DataDir, "workspaces", fmt.Sprintf("asof-run-%d", run))))
	}
	// Kept transcripts are named by their content, so a file another
	// project's receipt still names stays.
	note(workflow.RemoveUnreferencedTranscripts(ctx, c.DB, c.DataDir, plan.Transcripts))
	for _, step := range plan.PreviewSteps {
		if local {
			note(c.Docker.RemoveDisposableStack(ctx, docker.RunStepStackID(step)))
		}
	}
	for _, change := range plan.ChangeIDs {
		note(c.Previews.RemovePreviewForGood(ctx, docker.PreviewChangeName(change)))
		if local {
			note(c.Docker.RemoveDisposable(ctx, docker.PreviewChangeName(change)))
			failed = append(failed, maintenance.RemoveBuildImages(ctx, *c.Docker, docker.PreviewChangeName(change))...)
			note(c.Docker.RemoveDisposableStack(ctx, docker.ChangeStackID(change)))
		}
		note(os.RemoveAll(filepath.Join(c.DataDir, "workspaces", fmt.Sprintf("change-%d", change))))
		note(os.RemoveAll(filepath.Join(c.DataDir, "workspaces", fmt.Sprintf("asof-change-%d", change))))
	}
	// The GitLab copy is never destroyed: it is renamed out of the way,
	// freeing the path for a future project, and archived read-only.
	if plan.GitLabPath != "" {
		note(c.archiveGitLab(ctx, plan))
	}
	note(os.RemoveAll(filepath.Join(c.DataDir, "workspaces", "project-"+plan.Slug)))
	return failed
}

// archiveGitLab moves the deleted project's GitLab copy, the one the plan
// named by id, to its archive name and archives it; each attempt reads that
// same project and does only what is left. A refusal to archive (the token
// needs Owner) is a failure, never a quiet success.
func (c Cleanup) archiveGitLab(ctx context.Context, plan CleanupPlan) error {
	if plan.GitLabProjectID == 0 {
		return fmt.Errorf("the plan does not say which GitLab project was at %s; archive it by hand, then retry", plan.GitLabPath)
	}
	gl, err := c.GitLab.Client(ctx)
	if err != nil {
		return fmt.Errorf("the GitLab copy at %s was not archived: %w", plan.GitLabPath, err)
	}
	p, err := gl.Project(ctx, strconv.FormatInt(plan.GitLabProjectID, 10))
	if err != nil {
		return fmt.Errorf("reading GitLab project %d (%s): %w", plan.GitLabProjectID, plan.GitLabPath, err)
	}
	if path.Base(p.PathNS) != plan.GitLabArchive {
		if err := gl.RenameProject(ctx, p.ID, plan.GitLabArchive); err != nil {
			return fmt.Errorf("moving GitLab project %d from %s to %s: %w", p.ID, p.PathNS, plan.GitLabArchive, err)
		}
	}
	if !p.Archived {
		if err := gl.ArchiveProject(ctx, p.ID); err != nil {
			return fmt.Errorf("archiving GitLab project %d (AppTown's token needs Owner to archive): %w", p.ID, err)
		}
	}
	return nil
}
