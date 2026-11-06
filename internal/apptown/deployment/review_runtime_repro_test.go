package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/dockerlabel"
)

// A public part can already be stopped by the crash-loop health controller.
// If Stop cannot stop a remaining worker, status repair must retain uncertainty.
func TestReviewCheckDoesNotConfirmStopWithRunningWorker(t *testing.T) {
	reviewCheckPartialRuntime(t, "stop", false)
}

func TestReviewCheckDoesNotConfirmStartWithStoppedWorker(t *testing.T) {
	reviewCheckPartialRuntime(t, "start", false)
}

func TestReviewCheckDoesNotConfirmRestartWithStoppedWorker(t *testing.T) {
	reviewCheckPartialRuntime(t, "restart", false)
}

func TestRuntimeCheckRetainsTransitionWhenWorkerCannotBeAssessed(t *testing.T) {
	for _, action := range []string{"stop", "start", "restart"} {
		t.Run(action, func(t *testing.T) { reviewCheckPartialRuntime(t, action, true) })
	}
}

func reviewCheckPartialRuntime(t *testing.T, action string, unreadable bool) {
	t.Helper()
	s, dep, _, _ := runtimeFixture(t, KindDockerHost)

	at, _, err := s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "with-containers"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Outcomes.Complete(t.Context(), Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", Handle: map[string]any{"container": "public"}}}); err != nil {
		t.Fatal(err)
	}
	if action == "start" {
		if _, err := s.ControlRuntime(t.Context(), dep.ProjectID, dep.Name, "stop"); err != nil {
			t.Fatal(err)
		}
	}
	daemon := &reviewPartialStopDaemon{label: string(dep.Placement().Label), starting: action != "stop", unreadable: unreadable}
	driver := &docker.Driver{
		Scope: func(context.Context) (docker.Scope, error) { return docker.Scope{Client: daemon}, nil },
		Owned: func(context.Context) ([]deploy.Owned, error) {
			return []deploy.Owned{{Kind: "container", ID: "public"}, {Kind: "container", ID: "worker"}, {Kind: "container", ID: "db", Need: "postgres"}}, nil
		},
	}
	s.providers.drivers[KindDockerHost] = func(context.Context, *Target, *Deployment) (deploy.Driver, error) { return driver, nil }
	if _, err := s.ControlRuntime(t.Context(), dep.ProjectID, dep.Name, action); err == nil {
		t.Fatal("fault injection did not fail Stop")
	}
	current, err := s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.RuntimeState != map[string]string{"stop": "stopping", "start": "starting", "restart": "restarting"}[action] {
		t.Fatal(current.RuntimeState)
	}
	checkErr := s.checkRuntimeControl(t.Context(), current)
	current, err = s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if checkErr == nil || !current.runtimeBusy() || current.RuntimeError == "" {
		t.Fatalf("Check confirmed a half-finished %s: state=%s error=%v", action, current.RuntimeState, checkErr)
	}
	acts, err := audit.Recent(t.Context(), s.db, audit.Filter{ProjectID: dep.ProjectID, KindPrefix: "deployment.runtime-confirmed", Subject: dep.Name})
	if err != nil || len(acts) != 0 {
		t.Fatalf("false confirmation receipts: %+v %v", acts, err)
	}
	// Once every app part reaches the requested state, a running/stopped need
	// in the opposite state must not prevent confirmation.
	daemon.complete, daemon.unreadable = true, false
	if err := s.checkRuntimeControl(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	current, err = s.Deployments.ByID(t.Context(), dep.ID)
	want := "running"
	if action == "stop" {
		want = "stopped"
	}
	if err != nil || current.RuntimeState != want || current.RuntimeError != "" {
		t.Fatalf("complete app action: %+v %v", current, err)
	}
}

type reviewPartialStopDaemon struct {
	label                          string
	starting, unreadable, complete bool
}

func (d *reviewPartialStopDaemon) Run(_ context.Context, _ string, command docker.Command) ([]byte, error) {
	a := command.Args
	if a[0] == "inspect" {
		name := a[len(a)-1]
		if name == "worker" && d.unreadable {
			return nil, errors.New("worker inspection unavailable")
		}
		running := name == "worker" && !d.complete
		if d.starting {
			running = !running
		}
		if name == "db" {
			running = !d.starting
		}
		return json.Marshal(docker.Runtime{ID: name, Running: running, Labels: map[string]string{dockerlabel.Deployment: d.label}})
	}
	if a[0] == "stop" || a[0] == "start" {
		return nil, errors.New("daemon could not stop worker")
	}
	return nil, fmt.Errorf("unexpected command: %v", a)
}
