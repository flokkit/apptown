package maintenance

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/stock"
)

// The containers a step leaves, its own and its private daemon's, are aged
// on the clock that labelled them: AppTown's. With that clock three days
// ahead of the machine's, both are new when they are made and reapable
// three hours later by the same clock, as their owner step is gone.
func TestAStepsContainersAgeOnAppTownsClock(t *testing.T) {
	t.Parallel()
	made := time.Now().Add(72 * time.Hour).UTC()
	clockNow := func() time.Time { return made }

	// The step's container, as a recorded task decorates it.
	step := agentenv.Decorate(execution.Request{Name: "apptown-step-00000000-03e7-7000-8000-000000000000-a1"}, made, agentenv.ProjectCache(idstest.N(1)))
	// The step's private daemon, as the workshop starts it.
	var mu sync.Mutex
	var dindRun []string
	starting := dockerfake.New()
	starting.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "run" {
			mu.Lock()
			dindRun = append([]string(nil), args...)
			mu.Unlock()
		}
		return nil, nil, true
	})
	db := databaseSuite.New(t)
	workshop := agentenv.NewWorkshop(&docker.Scope{Client: starting}, integration.NewService(db, integration.Dependencies{}), nil, t.TempDir(), "apptown-agent", stock.Images().PrivateDocker, clockNow)
	if _, _, cleanup, err := workshop.StartStep(t.Context(), idstest.N(999), t.TempDir(), "net", ".docker"); err != nil {
		t.Fatal(err)
	} else {
		defer cleanup()
	}
	mu.Lock()
	dind := labelsOf(dindRun)
	mu.Unlock()
	rows := []string{
		strings.Join([]string{step.Name, "step", labelValue(step.Labels, "apptown.created"), labelValue(step.Labels, "apptown.owner"), "c-step"}, "\t"),
		strings.Join([]string{"apptown-dind-step-00000000-03e7-7000-8000-000000000000", "dind", labelValue(dind, "apptown.created"), labelValue(dind, "apptown.owner"), "c-dind"}, "\t"),
	}
	daemon := dockerfake.New()
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "ps" {
			return []byte(strings.Join(rows, "\n")), nil, true
		}
		return nil, nil, false
	})
	sessions := console.New(console.Dependencies{Agent: consoletest.NewAgent(), Config: console.Config{DataDir: t.TempDir()}})
	reaper := func(at time.Time) *Reaper {
		return New(db, Dependencies{ToolVersions: stock.Versions(), Docker: docker.Scope{Client: daemon}, Sessions: sessions, Now: func() time.Time { return at }}, t.TempDir())
	}
	if got := reaper(made).Containers(context.Background(), true); len(got) != 0 {
		t.Fatalf("reaped containers just made by AppTown's clock: %v", got)
	}
	if got := reaper(made.Add(3*time.Hour)).Containers(context.Background(), true); len(got) != 2 {
		t.Fatalf("would reap %v three hours later by AppTown's clock, want the step's and its daemon's", got)
	}
}

func labelsOf(args []string) []string {
	var out []string
	for i, a := range args {
		if a == "--label" && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func labelValue(labels []string, key string) string {
	for _, l := range labels {
		if k, v, ok := strings.Cut(l, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// The networks a step makes, its services' and its private daemon's, carry
// the class and owner the reaper selects by: a step whose record is gone
// leaves both reapable. Kills: a network made without its labels, which the
// reaper would never select and Docker's address pools would keep.
func TestAStepsNetworksCarryTheOwnerTheReaperReads(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	made := map[string][]string{} // network name -> its labels
	starting := dockerfake.New()
	starting.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 2 && args[0] == "network" && args[1] == "create" {
			mu.Lock()
			made[args[len(args)-1]] = labelsOf(args)
			mu.Unlock()
		}
		return nil, nil, true
	})
	scope := docker.Scope{Client: starting}
	db := databaseSuite.New(t)
	stepID := idstest.N(998) // no such step: its owner has finished
	network, cleanup, err := docker.StartServices(t.Context(), scope, stepID.String(), []deploy.ServiceSpec{{Name: "db", Image: "postgres:16"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	workshop := agentenv.NewWorkshop(&scope, integration.NewService(db, integration.Dependencies{}), nil, t.TempDir(), "apptown-agent", stock.Images().PrivateDocker, time.Now)
	_, dindNet, dindCleanup, err := workshop.StartStep(t.Context(), stepID, t.TempDir(), "", ".docker")
	if err != nil {
		t.Fatal(err)
	}
	defer dindCleanup()

	mu.Lock()
	var rows []string
	for _, n := range []string{network, dindNet} {
		labels, ok := made[n]
		if !ok {
			t.Fatalf("network %s was not made by this step: %v", n, made)
		}
		rows = append(rows, strings.Join([]string{"id-" + n, n, labelValue(labels, "apptown.class"), labelValue(labels, "apptown.owner")}, "\t"))
	}
	mu.Unlock()
	daemon := dockerfake.New()
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 1 && args[0] == "network" && args[1] == "ls" {
			return []byte(strings.Join(rows, "\n")), nil, true
		}
		return nil, nil, false
	})
	sessions := console.New(console.Dependencies{Agent: consoletest.NewAgent(), Config: console.Config{DataDir: t.TempDir()}})
	r := New(db, Dependencies{ToolVersions: stock.Versions(), Docker: docker.Scope{Client: daemon}, Sessions: sessions}, t.TempDir())
	if got := r.Containers(context.Background(), true); strings.Join(got, ",") != network+","+dindNet {
		t.Fatalf("would reap %v, want the step's networks %s and %s", got, network, dindNet)
	}
}
