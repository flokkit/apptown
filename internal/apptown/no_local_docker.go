package apptown

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	piharness "github.com/flokkit/apptown/internal/apptown/agentharness/pi"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/maintenance"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
)

// projectProcesses runs project processes on this host (agents, scripts,
// checks, drafts): AppTown's runner, refused on an AppTown with no local
// Docker. AppTown's own Git keeps processRunner.
func projectProcesses(a *App) execution.Runner {
	r := a.processRunner
	if a.cfg.noLocalDocker {
		r.Refused = docker.ErrNoLocalBackend
	}
	return r
}

// workerAgentLabels reads the agent image's labels from the worker, for a
// AppTown with no local Docker to read them from. A worker that does not
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

// drainLocalDocker is the end of startup repair under apptownd
// -drain-local-docker: the witnessed switch into the -no-local-docker mode.
// An AppTown in the mode cannot observe its own Docker, so it cannot
// tell from its records alone that nothing local still writes: a start
// without the mode repairs a running step's record without stopping its
// writer. The drain is run by an AppTown that still has its Docker, with its
// normal settings, while no other AppTown process serves this database. After
// the normal repair it requires that the records hold no local work
// (maintenance.LocalWorkLeft), then asks that Docker for AppTown's execution
// containers. Only when both are clear does it record local_docker = drained,
// which admits the next start in the mode; it never records it on the records
// alone. Nothing is stopped or removed here.
func drainLocalDocker(ctx context.Context, db *database.DB, records *tasks.Records, consoles *console.Service, projects *project.Projects, dk docker.Scope) error {
	if err := maintenance.LocalWorkLeft(ctx, db, records, consoles, projects, workflowAbandonmentJob, workflowCompletionJob); err != nil {
		return err
	}
	left, err := maintenance.ExecutionContainers(ctx, dk)
	if err != nil {
		return fmt.Errorf("-drain-local-docker: this AppTown's Docker could not be asked which execution containers remain, so no drain is recorded: %w", err)
	}
	if len(left) > 0 {
		return fmt.Errorf("-drain-local-docker: this AppTown's Docker still has execution containers:\n  %s\n"+
			"Start AppTown without -no-local-docker so it finishes or removes them, or remove them yourself once you know they have stopped, then run apptownd -drain-local-docker again", strings.Join(left, "\n  "))
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
	return fmt.Errorf("-no-local-docker: this AppTown last started with its own Docker (local_docker = %s), and an AppTown in the mode cannot see what is left there. "+
		"Run apptownd -drain-local-docker with your current settings, without -no-local-docker, first: it serves nothing, and records the drain once it has seen that no local work is left", last)
}
