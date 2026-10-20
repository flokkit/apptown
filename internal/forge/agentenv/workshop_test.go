package agentenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/execution"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/dbschema"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/progress"
)

var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M) { os.Exit(databaseSuite.Run(m)) }

type workshopClient struct {
	mu          sync.Mutex
	calls       []string
	failStart   bool
	inspections int
	preload     chan struct{}
}

func (c *workshopClient) Run(_ context.Context, _ string, req docker.Command) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	args := req.Args
	c.calls = append(c.calls, strings.Join(args, " "))
	if args[0] == "image" {
		c.inspections++
		if c.inspections == 2 {
			close(c.preload)
		}
		return nil, errors.New("image unavailable") // preload is optional
	}
	if args[0] == "run" && c.failStart {
		return []byte("start rejected"), errors.New("start rejected")
	}
	return nil, nil
}

// ran says some call contains part.
func (c *workshopClient) ran(part string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, call := range c.calls {
		if strings.Contains(call, part) {
			return true
		}
	}
	return false
}

func (c *workshopClient) called(prefix string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, call := range c.calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

func TestWorkshopCleanupOwnsOnlyItsNetwork(t *testing.T) {
	t.Parallel()
	for _, network := range []string{"", "services-network"} {
		for _, fail := range []bool{false, true} {
			name := network
			if name == "" {
				name = "owned"
			}
			if fail {
				name += "-failed"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				client := &workshopClient{failStart: fail, preload: make(chan struct{})}
				scope := docker.Scope{Client: client}
				feeds := &progress.Feeds{}
				integrations := integration.NewService(databaseSuite.New(t), integration.Dependencies{})
				workshop := NewWorkshop(&scope, integrations, feeds, t.TempDir(), "test-agent", nil)
				ws := t.TempDir()
				if err := os.MkdirAll(filepath.Join(ws, ".apptown"), 0755); err != nil {
					t.Fatal(err)
				}
				_, got, cleanup, err := workshop.StartStep(t.Context(), idstest.N(91), ws, network, ".apptown/custom-home/.docker", nil)
				if fail {
					if err == nil || cleanup != nil {
						t.Fatal("failed provision did not dispose its resources")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if network != "" && got != network {
						t.Fatal("did not join the supplied network")
					}
					cleanup()
					select {
					case <-client.preload:
					case <-time.After(time.Second):
						t.Fatal("preload worker did not finish")
					}
					if !client.called("exec -e DOCKER_HOST=tcp://localhost:2375 -e DOCKER_CONFIG=/work/.apptown/custom-home/.docker") {
						t.Fatal("Docker client config ignored the supplied harness location")
					}
				}
				if !client.called("rm -f -v forge-dind-step-00000000-005b-7000-8000-000000000000") {
					t.Fatal("sidecar was not removed")
				}
				if client.called("network rm ") != (network == "") {
					t.Fatal("cleanup removed a borrowed network or leaked its own")
				}
			})
		}
	}
}

// A step's private Docker is torn down though its setup's context has
// ended (a stopping forge): the teardown runs on a context of its own.
//
// Kills: the teardown run on the setup's context.
func TestAWorkshopIsTornDownAfterItsContextEnds(t *testing.T) {
	t.Parallel()
	client := &workshopClient{preload: make(chan struct{})}
	scope := docker.Scope{Client: client}
	integrations := integration.NewService(databaseSuite.New(t), integration.Dependencies{})
	workshop := NewWorkshop(&scope, integrations, &progress.Feeds{}, t.TempDir(), "test-agent", nil)
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".apptown"), 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	_, _, cleanup, err := workshop.StartStep(ctx, idstest.N(92), ws, "", ".apptown/home/.docker", nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.preload:
	case <-time.After(5 * time.Second):
		t.Fatal("preload worker did not finish")
	}
	cancel() // AppTown stops while the step runs
	cleanup()
	if !client.called("rm -f -v forge-dind-step-00000000-005c-7000-8000-000000000000") || !client.called("network rm forge-dind-step-00000000-005c-7000-8000-000000000000") {
		t.Fatal("the sandbox was not torn down after its context ended")
	}
}

// A step's private Docker, where its builds run, is bounded by AppTown's
// task limits and shares only its own project's cache, at the same path as
// the step's container, so a nested build sees what the step sees.
//
// Kills: a sidecar started with no limits and the one AppTown-cache volume.
func TestAStepsPrivateDockerIsBoundedAndSharesOnlyItsProjectsCache(t *testing.T) {
	t.Parallel()
	client := &workshopClient{preload: make(chan struct{})}
	scope := docker.Scope{Client: client}
	integrations := integration.NewService(databaseSuite.New(t), integration.Dependencies{})
	workshop := NewWorkshop(&scope, integrations, &progress.Feeds{}, t.TempDir(), "test-agent", nil)
	workshop.Limits = execution.Limits{CPUs: 3, MemoryBytes: 2 << 30, PIDs: 512}
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".apptown"), 0755); err != nil {
		t.Fatal(err)
	}
	_, _, cleanup, err := workshop.StartStep(WithCacheScope(t.Context(), "game"), idstest.N(92), ws, "", ".apptown/home/.docker", nil)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	<-client.preload
	if !client.ran("-v forge-cache-game:/apptown-cache --cpus 3 --memory 2147483648 --memory-swap 2147483648 --pids-limit 512") {
		t.Fatal("the private Docker runs without its project's cache or its limits")
	}
	if client.ran(" forge-cache:/apptown-cache") {
		t.Fatal("the private Docker mounts the shared cache")
	}
}
