package dataprotection

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/poll"
)

// A complete reading establishes the counter baseline. The effect then observes
// the owner's invalidation marker, which compares current and completed counters.
func expectHostAct(t *testing.T, s *both, targetID ids.ID) func() {
	t.Helper()
	target, err := s.targets.ByID(t.Context(), targetID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	loops := deployment.HostReaders{Snapshots: deployment.NewHostSnapshots(s.db, s.now), Targets: s.targets, Hosts: s.hosts, Providers: s.providers}
	go func() { defer close(done); loops.Run(ctx) }()
	ready := poll.Until(t.Context(), 5*time.Second, time.Millisecond, func() bool {
		r, err := s.InventoryFor(t.Context(), target)
		return err == nil && !r.Unknown && !r.Invalidated
	})
	cancel()
	<-done
	if !ready {
		r, err := s.InventoryFor(t.Context(), target)
		t.Fatalf("host did not complete baseline: %+v %v", r, err)
	}
	return func() {
		t.Helper()
		r, err := s.InventoryFor(t.Context(), target)
		if err != nil || !r.Invalidated {
			t.Errorf("effect preceded act counter: %+v %v", r, err)
		}
	}
}

func TestRemovalMarksHostBeforeStoppingParts(t *testing.T) {
	t.Parallel()
	s, client, dep := takeDownWorld(t, "act-removal")
	checkAct := expectHostAct(t, s, dep.TargetID)
	called := false
	client.SetCommandReply(func(req docker.Command) ([]byte, error, bool) {
		if req.Args[0] == "stop" {
			called = true
			checkAct()
		}
		if req.Stdout != nil {
			return nil, context.Canceled, true
		}
		return nil, nil, false
	})
	_, _ = s.Remove(t.Context(), removeSelection(dep))
	if !called {
		t.Fatal("no stop effect")
	}
}

func TestManagedCopyMarksRunnerBeforeCommand(t *testing.T) {
	t.Parallel()
	client := newDockerFake()
	w := newDataWorldWith(t, dataWorldOptions{docker: client})
	w.protect()
	runner, err := w.s.Targets.ByName(t.Context(), w.target.CopyRunner())
	if err != nil {
		t.Fatal(err)
	}
	checkAct := expectHostAct(t, w.s, runner.ID)
	dk, _, err := w.s.copyRunnerForAct(t.Context(), w.target)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	client.SetCommandReply(func(req docker.Command) ([]byte, error, bool) {
		called = true
		checkAct()
		return nil, nil, true
	})
	_, _ = dk.Run(t.Context(), "run", "copy-client")
	if !called {
		t.Fatal("no copy command")
	}
}

func TestRehearsalMarksHostBeforeStagingAndCleanup(t *testing.T) {
	t.Parallel()
	w := newSharedRehearsalWorld(t)
	checkAct := expectHostAct(t, w.s, w.remote.ID)
	stage := false
	old := w.s.hostScope
	w.s.hostScope = func(ctx context.Context, target *deployment.Target) (docker.Scope, error) {
		dk, err := old(ctx, target)
		dk.Client = actClient{client: dk.Client, before: func(req docker.Command) {
			if req.Args[0] == "run" && req.Stdin != nil && !stage {
				stage = true
				checkAct()
			}
		}}
		return dk, err
	}
	op := w.rehearse(t)
	if !endedAs(op, operationVerified) || !stage {
		t.Fatalf("staging: %v, operation: %s", stage, op.Detail)
	}
	checkClear := expectHostAct(t, w.s, w.remote.ID)
	clear := false
	w.s.hostScope = func(ctx context.Context, target *deployment.Target) (docker.Scope, error) {
		dk, err := old(ctx, target)
		dk.Client = actClient{client: dk.Client, before: func(req docker.Command) {
			if req.Args[0] == "rm" {
				clear = true
				checkClear()
			}
		}}
		return dk, err
	}
	w.s.clearPlace(t.Context(), rehearsalPlace{Host: w.remote.Name, App: "apptown-rehearsal-clean", Stack: "rehearsal-clean"})
	if !clear {
		t.Fatal("no cleanup effect")
	}
}

type actClient struct {
	client docker.Client
	before func(docker.Command)
}

func (c actClient) Run(ctx context.Context, host string, req docker.Command) ([]byte, error) {
	c.before(req)
	return c.client.Run(ctx, host, req)
}

func TestRestoredCopyRunnerMarksHostBeforeReconcile(t *testing.T) {
	t.Parallel()
	s, _, dep := takeDownWorld(t, "act-restored")
	checkAct := expectHostAct(t, s, dep.TargetID)
	called := false
	_, _ = s.runRestoredCopy(t.Context(), deployment.Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "restored-act"}, func(context.Context, deployment.Attempt, deploy.Checkpoint) (deploy.Observation, error) {
		called = true
		checkAct()
		return deploy.Observation{}, deploy.Terminal(context.Canceled)
	})
	if !called {
		t.Fatal("no reconcile effect")
	}
}
