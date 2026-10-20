package forge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/consoletest"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/stamp"
)

// The event-driven reapers decide from labels, never from name substrings,
// and they touch only the stack they are given: a reaper that swept every
// preview stack on the box would let finishing one run tear down another
// run's live preview services.

func TestReapRunContainersReapsOnlyThatRun(t *testing.T) {
	t.Parallel()
	f := newFakeDocker()
	a := testApp(t, Config{DockerClient: f})
	projectID := createReadTestProject(t, a, "p")
	installFakeGit(t, a, nil)
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "reap", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "verify", Steps: []schema.Step{{Name: "preview", Uses: "preview"}, {Name: "agent", Uses: "agent/pi"}}}}}})
	a.Work.Stop()
	start := func(title string) (ids.ID, ids.ID) {
		t.Helper()
		changeID := createReadTestChange(t, a, projectID, title)
		run, err := a.Work.StartRun(asPerson("bls"), workflow.StartChangeRun{ChangeID: changeID, Workflow: "reap"})
		if !errors.Is(err, workflow.ErrDispatchStopped) {
			t.Fatal(err)
		}
		snapshot, err := a.Work.Snapshot(asPerson("bls"), run.ID, workflow.DetailView)
		if err != nil {
			t.Fatal(err)
		}
		for _, step := range snapshot.Steps {
			if step.Type == "preview" {
				return run.ID, step.ID
			}
		}
		t.Fatal("declared preview was not materialized")
		return ids.Nil, ids.Nil
	}
	runA, stepA := start("reap this work")
	runB, stepB := start("keep this work")

	workResourcesFor(a).Run(context.Background(), runA)
	calls := f.joined()
	if len(calls) == 0 || calls[0] != "rm -f -v "+docker.PreviewRunName(runA) {
		t.Fatalf("first call must remove run A's preview container, got %v", calls)
	}
	if calls[1] != "volume rm "+docker.DataVolumeName(docker.PreviewRunName(runA)) {
		t.Fatalf("second call must remove run A's data volume, got %q", calls[1])
	}
	ownerA := "label=forge.owner=" + docker.RunStepStackID(stepA)
	ownerB := "label=forge.owner=" + docker.RunStepStackID(stepB)
	var sawA bool
	for _, c := range calls {
		if strings.HasPrefix(c, "ps") {
			if !strings.Contains(c, "label=forge.owner=") {
				t.Fatalf("an unscoped listing sweeps every stack on the box: %s", c)
			}
			if strings.Contains(c, ownerA) {
				sawA = true
			}
			if strings.Contains(c, ownerB) {
				t.Fatalf("run B's stack was touched while reaping run A: %s", c)
			}
		}
		if strings.Contains(c, "name=") || strings.Contains(c, "sh -c") {
			t.Fatalf("reaping by name substring or shell pipeline: %s", c)
		}
		if strings.Contains(c, docker.PreviewRunName(runB)) {
			t.Fatalf("run B's preview container was touched: %s", c)
		}
	}
	if !sawA {
		t.Fatalf("run A's preview-step stack was not reaped: %v", calls)
	}
	if got := f.matching("network rm "); len(got) != 1 || got[0] != "network rm "+docker.NetworkName(docker.RunStepStackID(stepA)) {
		t.Fatalf("exactly run A's stack network must go, got %v", got)
	}
}

// A change's worktree goes once the change is shipped or abandoned and a
// day has passed; a run's workspace once the run finished and a day has
// passed; the agent's session transcripts are kept first. Open work and
// project hubs stand.

func TestReapWorkspacesKeepsTranscripts(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	pid := createReadTestProject(t, a, "ws")
	closed := time.Now().Add(-2 * 24 * time.Hour)
	closedChange := createReadTestChange(t, a, pid, "old")
	if _, err := a.Work.Abandon(asPerson("bls"), workflow.StopWork{ChangeID: closedChange, Reason: "finished workspace"}); err != nil {
		t.Fatal(err)
	}
	// The wall-clock expiry boundary is private setup; closure is a real command.
	// sql-guarantee: Backdate only closed_at after real abandonment to cross the workspace retention boundary; Workflow closure always stamps database now and exposes no historical clock.
	completionExec(t, a.DB, `UPDATE changes SET closed_at=? WHERE id=?`, stamp.Of(closed), closedChange)
	open := createReadTestChange(t, a, pid, "live")
	mk := func(entry string, withSessions bool) string {
		ws := filepath.Join(a.cfg.dataDir, "workspaces", entry)
		if err := os.MkdirAll(filepath.Join(ws, ".apptown", "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(ws, "main.go"), []byte("package main"), 0o644)
		if withSessions {
			_ = os.WriteFile(filepath.Join(ws, ".apptown", "sessions", "step-00000000-0001-7000-8000-000000000000.jsonl"), []byte(`{"tool":"bash"}`), 0o644)
		}
		return ws
	}
	oldWS := mk(fmt.Sprintf("change-%d", closedChange), true)
	liveWS := mk(fmt.Sprintf("change-%d", open), true)
	hub := mk("project-ws-hub", false)
	// Log-only names it and touches nothing.
	if got := a.reaper.Workspaces(context.Background(), true); len(got) != 1 || got[0] != filepath.Base(oldWS) {
		t.Fatalf("log-only: %v", got)
	}
	if _, err := os.Stat(oldWS); err != nil {
		t.Fatal("log-only must not remove")
	}
	// A failed preservation must leave the source recoverable for a later sweep.
	blocked := filepath.Join(a.cfg.dataDir, "transcripts")
	if err := os.WriteFile(blocked, []byte("blocked destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.reaper.Workspaces(context.Background(), false)
	original := filepath.Join(oldWS, ".apptown", "sessions", "step-00000000-0001-7000-8000-000000000000.jsonl")
	if data, err := os.ReadFile(original); err != nil || string(data) != `{"tool":"bash"}` {
		t.Fatalf("failed preservation lost transcript: %q %v", data, err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	a.reaper.Workspaces(context.Background(), false)
	if _, err := os.Stat(oldWS); !os.IsNotExist(err) {
		t.Fatal("the closed change's worktree stands")
	}
	kept := filepath.Join(a.cfg.dataDir, "transcripts", filepath.Base(oldWS), "step-00000000-0001-7000-8000-000000000000.jsonl")
	if b, err := os.ReadFile(kept); err != nil || string(b) != `{"tool":"bash"}` {
		t.Fatalf("the transcript was not kept: %v", err)
	}
	for _, p := range []string{liveWS, hub} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s must stand: %v", p, err)
		}
	}
}

func TestReapWorkspacesRetainsRecoveredConversationCheckout(t *testing.T) {
	t.Parallel()
	agent := consoletest.NewAgent()
	a := testApp(t, Config{ConsoleAgent: agent})
	pid := createReadTestProject(t, a, "retained-chat")
	id := createReadTestChange(t, a, pid, "old")
	if _, err := a.Work.Abandon(asPerson("bls"), workflow.StopWork{ChangeID: id, Reason: "finished workspace"}); err != nil {
		t.Fatal(err)
	}
	// Age only the durable clock boundary; chat retention is exercised below.
	// sql-guarantee: Backdate only closed_at after real abandonment to cross the workspace retention boundary; Workflow closure always stamps database now and exposes no historical clock.
	completionExec(t, a.DB, `UPDATE changes SET closed_at=? WHERE id=?`, stamp.Of(time.Now().Add(-72*time.Hour)), id)
	ws := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", id))
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(ws, "uncommitted.txt")
	if err := os.WriteFile(canary, []byte("saved chat work"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A change console: its checkout is the change's worktree, which ending
	// the conversation keeps (a project console's snapshot goes with it).
	m := consoleMetaFor("retained", ids.Nil, id)
	m.Checkout, m.LastUsed = ws, time.Now().Add(-72*time.Hour)
	s := a.Console.Restore(m)
	if err := s.Persist(); err != nil {
		t.Fatal(err)
	}
	// Startup ends a chat on a closed change; this one's runtime cannot be
	// removed, so it stays live, and its checkout with it.
	agent.FailRemove("retained", 1)
	if err := a.RecoverConsoles(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, logOnly := range []bool{true, false, false} {
		if got := a.reaper.Workspaces(context.Background(), logOnly); len(got) != 0 {
			t.Fatalf("saved conversation checkout reaped: %v", got)
		}
		if raw, err := os.ReadFile(canary); err != nil || string(raw) != "saved chat work" {
			t.Fatalf("saved work changed: %v", err)
		}
	}
	// Ending this conversation releases ownership. A similar sibling path
	// belonging to another chat does not keep this workspace forever.
	if _, err := a.Console.End(context.Background(), "retained"); err != nil {
		t.Fatal(err)
	}
	sm := consoleMetaFor("sibling", ids.Nil, ids.Nil)
	sm.Checkout = ws + "-other"
	a.Console.Restore(sm)
	if got := a.reaper.Workspaces(context.Background(), false); len(got) != 1 {
		t.Fatalf("ended conversation still retains workspace: %v", got)
	}
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Fatalf("released workspace remains: %v", err)
	}
}

// The periodic reaper takes a step's containers and networks only once the
// step has finished, by the IDs it listed, and keeps them while the step is
// pending or running or its record cannot be read.
//
// Kills (networks): a reaper that never removes a finished step's network
// (a step's /16 once stayed for weeks), one that removes by
// name, one that selects by name rather than by label, one that ignores the
// owner's state, and one that takes a network whose owner it cannot read.
func TestReaperKeepsLongRunningExecutionResources(t *testing.T) {
	t.Parallel()
	f := newFakeDocker()
	a, _, step := attemptFixture(t, "script", Config{DockerClient: f})
	var execution *workflow.Execution
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Runners = map[string]workflow.Runner{"script": func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Fatal(err)
			}
			execution = task
		}}
	})
	a.Work.Stop()
	old := time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	rows := []string{}
	for i, owner := range []string{fmt.Sprintf("forge-step-%d-123", step.ID), fmt.Sprint(step.ID), fmt.Sprintf("forge-dind-step-%d", step.ID)} {
		class := []string{"step", "service", "dind"}[i]
		rows = append(rows, fmt.Sprintf("name-%d\t%s\t%s\t%s\told-id-%d", i, class, old, owner, i))
	}
	rows = append(rows, "unknown\tstep\t"+old+"\tunrecognised-owner\tunknown-id", "persistent\tservice\t"+old+"\tenv-project-prod\tstanding-id")
	// The step's network and its private daemon's, labelled with their
	// owner, go with it; one of an unrecognised owner, a standing
	// deployment's, a preview snapshot's, Docker's own and a step's network
	// made before the labels never.
	networks := []string{
		fmt.Sprintf("net-step\tforge-net-%d\tservice\t%d", step.ID, step.ID),
		fmt.Sprintf("net-dind\tforge-dind-step-%d\tdind\tforge-dind-step-%d", step.ID, step.ID),
		"net-unknown\tforge-net-unknown\tservice\tunrecognised-owner",
		"net-env\tforge-net-env-project-prod\tenv\tenv-project-prod", "net-snap\tforge-net-pvsnap-change-00000000-0009-7000-8000-000000000000\tservice\tpvsnap-change-00000000-0009-7000-8000-000000000000",
		"net-bridge\tbridge\t\t", fmt.Sprintf("net-old\tforge-net-%d\t\t", step.ID), fmt.Sprintf("net-old-dind\tforge-dind-step-%d\t\t", step.ID),
	}
	f.reply = func(args []string) (string, error, bool) {
		if args[0] == "ps" {
			return strings.Join(rows, "\n"), nil, true
		}
		if len(args) > 1 && args[0] == "network" && args[1] == "ls" {
			// As Docker answers: a label filter lists only networks that
			// carry the label; without it, every network, unlabelled ones
			// with empty label columns.
			listed := networks
			if strings.Contains(strings.Join(args, " "), "--filter label=forge.class") {
				listed = nil
				for _, n := range networks {
					if parts := strings.Split(n, "\t"); len(parts) > 2 && parts[2] != "" {
						listed = append(listed, n)
					}
				}
			}
			return strings.Join(listed, "\n"), nil, true
		}
		return "", nil, false
	}
	if got := a.reaper.Containers(context.Background(), false); len(got) != 0 {
		t.Fatalf("pending resources reaped: %v", got)
	}
	if err := a.Work.ExecuteStep(a.asEngine(t.Context()), step.ID); err != nil {
		t.Fatal(err)
	}
	if execution == nil {
		t.Fatal("runner was not entered")
	}
	if got := a.reaper.Containers(context.Background(), false); len(got) != 0 {
		t.Fatalf("running resources reaped: %v", got)
	}
	if err := execution.Finish(workflow.EncodeOutcome(workflow.StatusPass, nil, nil)); err != nil {
		t.Fatal(err)
	}
	allRows, allNetworks := rows, networks
	for _, only := range []string{"container", "network"} {
		rows, networks = nil, nil
		if only == "container" {
			rows = allRows[:1]
		} else {
			networks = allNetworks[:1]
		}
		dbtest.FailNextStatement(t, a.DB, "-- name: StepByID")
		if got := a.reaper.Containers(context.Background(), false); len(got) != 0 {
			t.Fatalf("failed ownership read authorised reaping a %s: %v", only, got)
		}
	}
	rows, networks = allRows, allNetworks
	if got := a.reaper.Containers(context.Background(), false); len(got) != 5 {
		t.Fatalf("finished resources: %v", got)
	}
	if removed := f.matching("network rm "); strings.Join(removed, ",") != "network rm net-step,network rm net-dind" {
		t.Fatalf("networks removed: %v", removed)
	}
	removed := f.matching("rm -f ")
	if len(removed) != 3 {
		t.Fatalf("removed: %v", removed)
	}
	for i, call := range removed {
		if call != fmt.Sprintf("rm -f old-id-%d", i) {
			t.Fatalf("removed by reusable name: %v", removed)
		}
	}
}

// A forge that stopped during a boot leaves the boot's own Docker
// configuration, with its registry login, in the boots' folder; starting
// again removes every one, since nothing boots yet.
func TestStartingRemovesBootConfigurationsAStopLeft(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{DockerClient: newFakeDocker()})
	left, _, err := a.docker.WithBootConfig(t.Context(), a.hosts.BootScratch(), &docker.RegistryCredential{Host: "r.example", Username: "u", Token: "left-token"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(left.Config, "config.json")); err != nil {
		t.Fatal(err)
	}
	a.ReapPreviewOrphans(context.Background())
	if _, err := os.Stat(left.Config); !os.IsNotExist(err) {
		t.Fatalf("a boot's configuration outlived AppTown's start: %v", err)
	}
}

// An idle chat's runtime is taken once. The chat stays registered and idle
// afterwards, and a later pass leaves it alone: with nothing of it on the
// host, it neither reports the chat as reaped nor asks the runtime to
// remove it again.
//
// Kills: a pass that reported "reaping" an evicted chat every five minutes
// forever.
func TestReaperEvictsAnIdleChatOnce(t *testing.T) {
	t.Parallel()
	agent := consoletest.NewAgent()
	// The reaper asks the host's Docker first; CI's unit job has none, so the
	// test gives it the fake daemon.
	a := testApp(t, Config{ConsoleAgent: agent, DockerClient: newFakeDocker()})
	m := consoleMetaFor("idle", idstest.N(1), ids.Nil)
	m.LastUsed = time.Now().Add(-3 * time.Hour)
	a.Console.Restore(m)
	if err := agent.Start(t.Context(), console.Launch{ID: "idle"}); err != nil {
		t.Fatal(err)
	}
	if got := a.reaper.Containers(t.Context(), false); len(got) != 1 || got[0] != "forge-console-idle" || agent.Running("idle") {
		t.Fatalf("idle chat not reaped: %v", got)
	}
	if _, err := a.Console.Get(t.Context(), "idle"); err != nil {
		t.Fatalf("reaping ended the chat: %v", err)
	}
	// A removal the pass asked for would consume this armed failure.
	agent.FailRemove("idle", 1)
	if got := a.reaper.Containers(t.Context(), false); len(got) != 0 {
		t.Fatalf("an evicted chat reported reaped again: %v", got)
	}
	if err := agent.Remove(t.Context(), "idle"); err == nil {
		t.Fatal("the second pass asked the runtime to remove an evicted chat")
	}
}
