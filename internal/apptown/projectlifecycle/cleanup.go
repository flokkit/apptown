package projectlifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/strs"
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

// cleanupSessions lists and removes the deleted project's console sessions.
type cleanupSessions interface {
	List(ctx context.Context, m console.Match) []*console.Session
	Remove(ctx context.Context, id string) error
	RemoveEnded(m console.Match) error
}

// previewCleaner retires the previews named by the frozen cleanup inventory.
// None of these operations requires the deleted run or change records.
type previewCleaner interface {
	RemoveReleaseForGood(context.Context, ids.ID, string, int64) error
	RemoveRunForGood(context.Context, ids.ID) error
	RemoveRunStepsForGood(context.Context, []ids.ID) error
	RemoveChangeForGood(context.Context, ids.ID) error
}

// gitLabClient verifies the saved identity using one current credential snapshot.
type gitLabClient interface {
	VerifyRecorded(context.Context, scm.RepositoryIdentity) (scm.VerifiedRepository, error)
}

// Cleanup carries out a recorded cleanup plan. It never deletes records and
// never guesses at resources: it acts only on the identifiers the deletion
// fixed, every effect treats an already-absent resource as done, and the
// project's slug stays held until the cleanup is done, so a new project of
// the same name cannot be touched.
type Cleanup struct {
	Projects *project.Projects
	Sessions cleanupSessions
	// Docker is used only by the project-cache owner; Preview owns preview resources.
	Docker *docker.Scope
	// Previews owns local and worker preview resources and their snapshots.
	Previews previewCleaner
	GitLab   gitLabClient
	DB       *database.DB
	DataDir  string
	// Now is AppTown's clock, which stamps the cleanup's receipts and
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
	failures := c.Clean(ctx, project.RepositoryRef{ID: d.ProjectID, Slug: d.Slug}, plan)
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
func (c Cleanup) Clean(ctx context.Context, owner project.RepositoryRef, plan CleanupPlan) []string {
	repo, err := owner.Path(c.DataDir)
	if err != nil {
		return []string{err.Error()}
	}
	inventory := plan.ReleasePreviews
	if inventory == nil {
		return []string{"historical release preview inventory is missing; inspect the old cleanup's original release resources before resolving it; retry alone cannot reconstruct their ownership"}
	}
	if inventory.Version != 1 || inventory.ProjectID != owner.ID || !project.ValidSlug(inventory.ServingLabel) {
		return []string{"invalid retained release preview ownership"}
	}
	for _, number := range inventory.Numbers {
		if number <= 0 {
			return []string{"invalid retained release preview number"}
		}
	}
	id := owner.ID
	var failed []string
	note := func(err error) {
		if err != nil {
			failed = append(failed, err.Error())
		}
	}
	// Removing a chat ends it, and its end records its spend first: a chat
	// whose spend could not be recorded is not removed.
	for _, s := range c.Sessions.List(ctx, conversation.ForProject(id)) {
		note(c.Sessions.Remove(ctx, s.ID))
	}
	// Ended chats are folders the registry no longer holds: their words and
	// pictures go with the project.
	note(c.Sessions.RemoveEnded(conversation.ForProject(id)))
	// With no local Docker nothing local was left (the mode's cutover
	// precondition), so its names are skipped, never failed on.
	local := !c.Docker.Absent()
	if local {
		note(agentenv.RemoveProjectCache(ctx, *c.Docker, id))
	}
	for _, run := range plan.RunIDs {
		note(c.Previews.RemoveRunForGood(ctx, run))
		note(os.RemoveAll((workflow.Layout{DataDir: c.DataDir}).RunWorkspace(run)))
	}
	// Kept transcripts are named by their content, so a file another
	// project's receipt still names stays.
	note(workflow.RemoveUnreferencedTranscripts(ctx, c.DB, c.DataDir, plan.Transcripts))
	note(c.Previews.RemoveRunStepsForGood(ctx, plan.PreviewSteps))
	for _, change := range plan.ChangeIDs {
		note(c.Previews.RemoveChangeForGood(ctx, change))
		note(os.RemoveAll((workflow.Layout{DataDir: c.DataDir}).ChangeWorkspace(change)))
	}
	for _, number := range inventory.Numbers {
		note(c.Previews.RemoveReleaseForGood(ctx, owner.ID, inventory.ServingLabel, number))
	}
	// The GitLab copy is never destroyed: it is renamed out of the way,
	// freeing the path for a future project, and archived read-only.
	if plan.GitLabPath != "" {
		if plan.RepositoryIdentity != nil && plan.RepositoryIdentity.ProjectID != owner.ID {
			note(fmt.Errorf("cleanup repository identity belongs to another project"))
		} else {
			note(c.archiveGitLab(ctx, plan))
		}
	}
	note(os.RemoveAll(repo))
	return failed
}

// archiveGitLab moves the deleted project's GitLab copy, the one the plan
// named by id, to its archive name and archives it; each attempt reads that
// same project and does only what is left. A refusal to archive (the token
// needs Owner) is a failure, never a quiet success.
func (c Cleanup) archiveGitLab(ctx context.Context, plan CleanupPlan) error {
	if plan.RepositoryIdentity == nil || plan.RepositoryIdentity.ProviderProjectID != plan.GitLabProjectID {
		return fmt.Errorf("the cleanup plan has no verified provider identity for %s; inspect its original provider before retrying", plan.GitLabPath)
	}
	if c.GitLab == nil {
		return fmt.Errorf("repository cleanup verifier is unavailable")
	}
	verified, err := c.GitLab.VerifyRecorded(ctx, *plan.RepositoryIdentity)
	if err != nil {
		return fmt.Errorf("the GitLab copy at %s was not archived: %w", plan.GitLabPath, err)
	}
	gl, p := verified.Provider.Client, verified.Observed
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
