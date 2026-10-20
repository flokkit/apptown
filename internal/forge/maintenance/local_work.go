package maintenance

import (
	"context"
	"flokkit.io/forge/internal/dockerlabel"
	"fmt"
	"strings"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/tasks"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

// ---- local work -------------------------------------------------------------
//
// What project work this forge's own host may still hold: the records and
// the containers the reaper reads too. The forge's drain into the
// -no-local-docker mode (design/operations/workers.md) asks both and keeps the mode record itself.

// WorkerBackend begins the backend recorded for a task run on a worker.
const WorkerBackend = "worker at "

// LocalWorkLeft is the records half of the cutover precondition of
// -no-local-docker (R04), checked by the drain after startup repair: a task
// attempt not on a worker that is not settled and released; a step recorded
// running with no task attempt held for it (agents and reviews run on this
// host without one); a chat placed on the forge that has not ended; a
// deleted project whose cleanup is not done; or a workflow cleanup, a job of
// one of cleanupJobs keyed ".../cleanup", that has not completed. Nothing is
// marked stopped or cleaned here.
func LocalWorkLeft(ctx context.Context, db *database.DB, records *tasks.Records, sessions *console.Service, projects *project.Projects, cleanupJobs ...string) error {
	var left []string
	attempts, err := records.Held(ctx)
	if err != nil {
		return err
	}
	held := map[ids.ID]bool{}
	for _, at := range attempts {
		held[at.Owner.StepID] = true
		if !strings.HasPrefix(at.Backend, WorkerBackend) {
			left = append(left, fmt.Sprintf("task %s (%s) on %s", at.Operation, at.Observed, at.Backend))
		}
	}
	steps, err := workflow.RunningStepsIn(ctx, db)
	if err != nil {
		return err
	}
	for _, s := range steps {
		// A call runs nothing itself; its child run's steps are listed.
		if s.Type != "workflow/call" && !held[s.ID] {
			left = append(left, fmt.Sprintf("step %d (%s) of run %d, running with no task record", s.ID, s.Type, s.RunID))
		}
	}
	chats, err := sessions.Unended()
	if err != nil {
		return err
	}
	for _, meta := range chats {
		if !conversation.OnWorkerRecord(meta) {
			left = append(left, "chat "+meta.ID+" on AppTown, not ended")
		}
	}
	deletions, err := projects.OutstandingDeletions(ctx)
	if err != nil {
		return err
	}
	for _, d := range deletions {
		left = append(left, fmt.Sprintf("cleanup of deleted project %s (%s), %s", d.Slug, d.ProjectID, d.State))
	}
	// A workflow cleanup's job is its only record: only completion is done.
	cleanups, err := database.Transact(ctx, db, func(tx *database.Tx) (out []string, err error) {
		for _, kind := range cleanupJobs {
			keys, err := jobs.IncompleteKeysIn(ctx, tx.Native(), kind)
			if err != nil {
				return nil, err
			}
			for _, key := range keys {
				if strings.HasSuffix(key, "/cleanup") {
					out = append(out, kind+" "+key+", not completed")
				}
			}
		}
		return out, nil
	})
	if err != nil {
		return err
	}
	if left = append(left, cleanups...); len(left) == 0 {
		return nil
	}
	return fmt.Errorf("-drain-local-docker: this AppTown's records hold local work that may still be running on its own Docker:\n  %s\n"+
		"Start AppTown without -no-local-docker so it settles and removes that work (end those chats, let the runs finish or stop them, and retry any cleanup waiting for a person), stop it, then run apptownd -drain-local-docker again", strings.Join(left, "\n  "))
}

// ExecutionContainers lists the containers on the forge's own Docker that
// ran or run project work: those carrying a label the forge's execution
// owners and reapers use (forge.class, a task's forge.task, a step's
// forge.step, a folder writer's forge.writes), stopped or not. Two classes
// are not writers and are left out: env, the release containers of
// deployments on the built-in target (out of the mode's scope, which refuses
// releasing there), and infra, the built-in database. Containers of another
// installation on the same daemon are listed too: the drain cannot tell them
// from this forge's, so it does not guess.
func ExecutionContainers(ctx context.Context, dk docker.Scope) ([]string, error) {
	out, err := dk.Command(ctx, docker.Command{Args: []string{"ps", "-a", "--format",
		`{{.Names}}\t{{.Label "` + dockerlabel.Class + `"}}\t{{.Label "` + dockerlabel.Task + `"}}\t{{.Label "` + dockerlabel.Step + `"}}\t{{.Label "` + docker.WritesLabel + `"}}`}})
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	var left []string
	for _, line := range strings.Split(string(out), "\n") {
		// Empty labels are empty fields, and trailing ones may be trimmed.
		name, labels, _ := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		class, _, _ := strings.Cut(labels, "\t")
		if name == "" || class == "env" || class == "infra" || strings.Trim(labels, "\t ") == "" {
			continue
		}
		what := name
		if class != "" {
			what += " (" + class + ")"
		}
		left = append(left, what)
	}
	return left, nil
}

// RemoveBuildImages removes the build images made for one container (a
// preview's), naming each removal that failed. An image a container still
// uses stays: the removal never forces.
func RemoveBuildImages(ctx context.Context, dk docker.Scope, container string) []string {
	refs, err := dk.BuildImages(ctx, container)
	if err != nil {
		return []string{err.Error()}
	}
	var failed []string
	for _, ref := range refs {
		if err := dk.RemoveUnusedImage(ctx, ref); err != nil {
			failed = append(failed, err.Error())
		}
	}
	return failed
}
