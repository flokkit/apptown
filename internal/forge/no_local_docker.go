package forge

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/execution"
	piharness "flokkit.io/forge/internal/forge/agentharness/pi"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/maintenance"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/tasks"
)

// projectProcesses runs project processes on this host (agents, scripts,
// checks, drafts): the forge's runner, refused on a forge with no local
// Docker. The forge's own Git keeps processRunner.
func projectProcesses(a *App) execution.Runner {
	r := a.processRunner
	if a.cfg.noLocalDocker {
		r.Refused = docker.ErrNoLocalBackend
	}
	return r
}

// workerAgentLabels reads the agent image's labels from the worker, for a
// forge with no local Docker to read them from. A worker that does not
// answer gives no labels, so nothing is promised that cannot be checked.
// life is the App's: the labels are read when a home is prepared, by
// callers with no context to give.
func workerAgentLabels(life context.Context, w *execution.WorkerTasks) func() piharness.Labels {
	return func() piharness.Labels {
		ctx, cancel := context.WithTimeout(life, 10*time.Second)
		defer cancel()
		image, err := w.AgentImage(ctx)
		if err != nil {
			log.Printf("reading the worker's agent image labels: %v", err)
		}
		return piharness.MapLabels(image.Labels)
	}
}

// drainLocalDocker is the end of startup repair under forged
// -drain-local-docker: the witnessed switch into the -no-local-docker mode.
// A forge in the mode cannot observe its own Docker, so it cannot
// tell from its records alone that nothing local still writes: a start
// without the mode repairs a running step's record without stopping its
// writer. The drain is run by a forge that still has its Docker, with its
// normal settings, while no other forge process serves this database. After
// the normal repair it requires that the records hold no local work
// (maintenance.LocalWorkLeft), then asks that Docker for the forge's execution
// containers. Only when both are clear does it record local_docker = drained,
// which admits the next start in the mode; it never records it on the records
// alone. Nothing is stopped or removed here.
func drainLocalDocker(ctx context.Context, db *database.DB, records *tasks.Records, consoles *console.Service, projects *project.Projects, dk docker.Scope) error {
	if err := maintenance.LocalWorkLeft(ctx, db, records, consoles, projects, workflowAbandonmentJob, workflowCompletionJob); err != nil {
		return err
	}
	left, err := maintenance.ExecutionContainers(ctx, dk)
	if err != nil {
		return fmt.Errorf("-drain-local-docker: this forge's Docker could not be asked which execution containers remain, so no drain is recorded: %w", err)
	}
	if len(left) > 0 {
		return fmt.Errorf("-drain-local-docker: this forge's Docker still has execution containers:\n  %s\n"+
			"Start the forge without -no-local-docker so it finishes or removes them, or remove them yourself once you know they have stopped, then run forged -drain-local-docker again", strings.Join(left, "\n  "))
	}
	return identity.RecordLocalDockerDrainedIn(ctx, db)
}

// switchAdmitted admits a start in the mode: after a start in it ("none"),
// after a witnessed drain ("drained"), or on a database no run was ever
// recorded in (no mark; migration 102 marked every earlier one "used").
func switchAdmitted(ctx context.Context, db *database.DB) error {
	last, err := identity.InstanceSettingIn(ctx, db, identity.SettingLocalDocker)
	if err != nil {
		return err
	}
	switch last {
	case identity.LocalDockerNone, identity.LocalDockerDrained, "":
		return nil
	}
	return fmt.Errorf("-no-local-docker: this forge last started with its own Docker (local_docker = %s), and a forge in the mode cannot see what is left there. "+
		"Run forged -drain-local-docker with your current settings, without -no-local-docker, first: it serves nothing, and records the drain once it has seen that no local work is left", last)
}
