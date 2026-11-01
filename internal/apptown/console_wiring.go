package apptown

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/changeconsole"
	"github.com/flokkit/apptown/internal/apptown/datalayout"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/maintenance"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"

	"github.com/flokkit/apptown/internal/apptown/conversation"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/pi"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/worker"
)

// Console is built once. AppTown owns the default Pi runtime and always supplies
// the launch environment; an injected runtime remains owned by its caller.
func (a *App) consoleDependencies(agent console.Agent, remote *worker.RemoteAgent) console.Dependencies {
	if a.cfg.noLocalDocker {
		// An AppTown with no local Docker runs no chat here; the chats it
		// once ran were ended before the switch (design/operations/workers.md).
		agent = console.NoLocal{}
	} else if agent == nil {
		image := a.cfg.agentImage
		scope := &a.docker
		a.consoleRuntime = pi.New(pi.Config{
			Run: func(ctx context.Context, args ...string) ([]byte, error) {
				return scope.Command(ctx, docker.Command{Args: args})
			},
			Image:  func() string { return image },
			Dind:   pi.DindConfig{Image: a.images.PrivateDocker},
			Limits: &pi.Limits{CPUs: a.cfg.chatLimits.CPUs, MemoryBytes: a.cfg.chatLimits.MemoryBytes, PIDs: a.cfg.chatLimits.PIDs},
			// Each chat's own cache, as on a worker: no chat shares a
			// writable package cache with another project's work.
			CachePerSession: true,
			Now:             a.cfg.clock.Now,
			LocalTLS:        a.cfg.localTLS,
		})
		agent = a.consoleRuntime
	}
	// With a worker, a chat whose record places it there runs there; every
	// other runs here.
	if remote == nil && a.cfg.worker != nil {
		image, err := worker.AgentImage(a.cfg.agentContext)
		if err != nil {
			log.Printf("chats on the worker will be refused: %v", err)
		}
		remote = &worker.RemoteAgent{Sessions: worker.SessionClient{URL: a.cfg.worker.URL, Client: a.cfg.worker.Client},
			Image: image, Root: filepath.Join(a.cfg.dataDir, "console"), Now: a.cfg.clock.Now}
	}
	if remote != nil {
		a.workerChats = remote
		sessions := &a.Console
		agent = console.Placed{Local: agent, Remote: a.workerChats, OnRemote: func(id string) (bool, error) {
			meta, err := (*sessions).Recorded(id)
			if errors.Is(err, console.ErrNoSession) {
				return false, nil
			}
			return err == nil && conversation.OnWorkerRecord(meta), err
		}}
	}
	ledger := a.Usage.Ledger
	return console.Dependencies{
		Agent:       agent,
		Environment: conversation.NewEnvironment(a.DB, conversation.EnvironmentDependencies{Projects: a.Projects, Models: a.modelSelection, Bindings: a.Integrations.Bindings, Instructions: a.steering, Home: piHomeFor(a), CLI: a.cli, Credentials: a.providers, Identity: a.Access, Now: a.cfg.clock.Now}, conversation.EnvironmentConfig{DataDir: a.cfg.dataDir, APIURL: a.cfg.consoleAPIURL, InferenceEnabled: a.cfg.inferenceURL != "", NoPrivateDocker: !a.cfg.privateDocker}),
		// An end records the chat's spend from its last record before the
		// record is archived or removed.
		Settle: func(ctx context.Context, s *console.Session) error {
			return conversation.HarvestUsage(ctx, ledger, s)
		},
		Config: console.Config{DataDir: a.cfg.dataDir, NoDind: !a.cfg.privateDocker, Mirror: a.workshop.Mirror, Now: a.cfg.clock.Now},
	}
}

// --- AppTown's rounds over consoles: boot, the five-minute tick, shutdown ---------

// RecoverConsoles restores the sessions on disk after a restart, harvests
// their usage, and starts the five-minute round.
//
// It runs before any work is resumed, and fails when a chat that does not
// hold its change cannot be ended while another worker holds that change:
// the two would share the change's folder.
func (a *App) RecoverConsoles(ctx context.Context) error {
	// A chat that could not be restored, or a runtime the reaper could not
	// remove, is still found by its folder label when anyone takes its
	// change (workflow.HolderEndedIn), so a reaper failure is logged, not
	// fatal.
	if err := a.Console.Resume(ctx); err != nil {
		log.Printf("console recovery: %v", err)
	}
	if err := a.changeConsole.EndChatsWithoutTheirChange(ctx); err != nil {
		return err
	}
	for _, s := range a.Console.List(ctx, console.Match{}) {
		if !conversation.Of(s).ChangeID.IsZero() {
			go a.changeConsole.Rehearse(ctx, s)
		}
	}
	a.harvestAllConsoleUsage(ctx)
	go a.consoleRounds(a.life, a.consoleStop)
	return nil
}

// consoleRounds is the five-minute tick: harvest usage, stop the idle.
func (a *App) consoleRounds(ctx context.Context, stop <-chan struct{}) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			a.harvestAllConsoleUsage(ctx)
			a.consoleIdleStop(ctx)
		}
	}
}

const consoleStopAfter = 30 * time.Minute // no viewers + idle → the agent stops

// consoleIdleStop walks the sessions down the ladder: idle past the
// deadline with nobody watching, the agent stops and the files stay. Every
// live record is refreshed on the way past.
func (a *App) consoleIdleStop(ctx context.Context) {
	now := a.cfg.clock.Now()
	for _, s := range a.Console.List(ctx, console.Match{}) {
		// Best effort, as on every save: a failure keeps the last record.
		if err := s.Persist(); err != nil {
			log.Printf("console %s: refreshing its record: %v", s.ID, err)
		}
		if s.Idle(now, consoleStopAfter) {
			if err := a.Console.Stop(ctx, s.ID); err != nil {
				log.Printf("console %s idle stop: %v", s.ID, err)
			}
		}
	}
}

// stopConsoles ends the console rounds for shutdown: the sessions'
// containers keep running and their records stay for the next recovery.
func (a *App) stopConsoles(ctx context.Context) {
	if a.consoleStop != nil {
		a.consoleStopOnce.Do(func() { close(a.consoleStop) })
	}
	if a.Console != nil {
		_ = a.Console.Shutdown(ctx)
	}
	if a.consoleRuntime != nil {
		a.consoleRuntime.Close()
	}
	if a.workerChats != nil {
		a.workerChats.Close()
	}
}

// --- usage: read from the session record, never by sniffing the terminal --------

// harvestAllConsoleUsage sweeps every session (the round, and startup).
func (a *App) harvestAllConsoleUsage(ctx context.Context) {
	// A failed harvest leaves its cursor where it was: the next round reads
	// the same spend again.
	for _, s := range a.Console.List(ctx, console.Match{}) {
		if err := conversation.HarvestUsage(ctx, a.Usage.Ledger, s); err != nil {
			log.Printf("chat usage: %v", err)
		}
	}
}

// changeConsoleFor is assembly only; the returned owner retains no App.
func changeConsoleFor(a *App) *changeconsole.Service {
	return changeconsole.New(a.chatSaves, changeconsole.Dependencies{DB: a.DB, Sessions: a.Console, Chats: chatsFor(a), Previews: a.Preview,
		Narratives: a.narratives, Folders: a.preparation, DataDir: a.cfg.dataDir, Stop: a.consoleStop, Engine: a.asEngine(a.life)})
}

// chatsFor is who may look at and act on a chat, with the checked authority.
func chatsFor(a *App) conversation.Chats {
	return conversation.Chats{Sessions: a.Console, DB: a.DB, Now: a.cfg.clock.Now,
		Authority: access.Checked{Authority: identity.Scoped(a.Access, "chat"), Projects: a.Projects}}
}

func consoleAdmissionFor(a *App) *conversation.Admission {
	return conversation.NewAdmission(a.DB, conversation.AdmissionDependencies{Work: a.Work, Sessions: a.Console, Models: a.modelSelection, Briefing: a.briefing, Harness: piHomeFor(a), Effects: a.changeConsole, Presence: holderPresenceFor(a), Workers: a.changeConsole.Placement(), NoLocal: a.cfg.noLocalDocker, Now: a.cfg.clock.Now}, a.cfg.dataDir)
}

// RelocateDataLayout repairs a data directory restored from originalRoot into
// another path; with the same root it does nothing. It assembles
// observation-only adapters before New can create application recovery
// state. The caller holds exclusive database ownership.
// A missing local backend stays unavailable; NoLocal's absent answer is not
// evidence that a previously local writer stopped.
func RelocateDataLayout(ctx context.Context, db *database.DB, input Config, originalRoot string) error {
	cfg, err := input.prepare()
	if err != nil {
		return err
	}
	instance, err := instanceID(cfg.dataDir)
	if err != nil {
		return err
	}
	observers := datalayout.Observers{Local: input.ConsoleAgent}
	if observers.Local == nil && !cfg.noLocalDocker {
		client := input.DockerClient
		if client == nil {
			client = docker.CLI{}
		}
		scope := docker.Scope{Client: client, Now: cfg.clock.Now}
		runtime := pi.New(pi.Config{Run: func(ctx context.Context, args ...string) ([]byte, error) {
			return scope.Command(ctx, docker.Command{Args: args})
		}, Now: cfg.clock.Now})
		defer runtime.Close()
		observers.Local = runtime
	}
	if input.ConsoleWorker != nil {
		observers.Remote = input.ConsoleWorker
	} else if cfg.worker != nil {
		remote := &worker.RemoteAgent{Sessions: worker.SessionClient{URL: cfg.worker.URL, Client: cfg.worker.Client}, Root: filepath.Join(cfg.dataDir, "console"), Now: cfg.clock.Now}
		defer remote.Close()
		observers.Remote = remote
	}
	if cfg.worker != nil {
		observers.Tasks = *cfg.worker
		observers.TaskBackend = maintenance.WorkerBackend + cfg.worker.URL
	} else if !cfg.noLocalDocker {
		namespace := taskNamespace(cfg.dataDir)
		daemon := execution.SelectDocker()
		observers.Tasks = execution.DockerTasks{Namespace: namespace, Daemon: daemon, Files: filepath.Join(cfg.dataDir, "task-files"), Now: cfg.clock.Now}
		observers.TaskBackend = "local-docker " + namespace + " on " + daemon.Backend()
	}
	return datalayout.Relocate(ctx, db, cfg.dataDir, originalRoot, instance, observers)
}

// FinalizeDataLayout finishes relocation journal cleanup after any restore continuation
// has settled. The caller still holds the exclusive database lock.
func FinalizeDataLayout(dataDir string) error {
	instance, err := instanceID(dataDir)
	if err != nil {
		return err
	}
	return datalayout.Finalize(dataDir, instance)
}
