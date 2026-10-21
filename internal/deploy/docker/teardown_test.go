package docker_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockerfake"
)

// A step's services are torn down though the step's context has ended (a
// stopping forge): the returned teardown runs on a context of its own.
//
// Kills: the teardown run on the setup's context.
func TestStartedServicesAreTornDownAfterTheirContextEnds(t *testing.T) {
	t.Parallel()
	daemon := dockerfake.New()
	var mu sync.Mutex
	var asked []string
	dk := docker.Scope{Client: scopeClientFunc(func(ctx context.Context, host string, req docker.Command) ([]byte, error) {
		mu.Lock()
		asked = append(asked, strings.Join(req.Args, " "))
		mu.Unlock()
		return daemon.Run(ctx, host, req)
	})}
	ctx, cancel := context.WithCancel(t.Context())
	network, cleanup, err := docker.StartServices(ctx, dk, "torn", []deploy.ServiceSpec{{Name: "db", Image: "db:1"}})
	if err != nil {
		t.Fatal(err)
	}
	cancel() // AppTown stops while the step runs
	mu.Lock()
	before := len(asked)
	mu.Unlock()
	cleanup()
	mu.Lock()
	teardown := strings.Join(asked[before:], "\n")
	mu.Unlock()
	if !strings.Contains(teardown, "network rm "+network) || !strings.Contains(teardown, "rm -f") {
		t.Fatalf("the step's services were not torn down after its context ended: %q", teardown)
	}
	if names := dk.Lines(context.Background(), "ps", "-aq"); len(names) != 0 {
		t.Fatalf("the step's services outlived their teardown: %v", names)
	}
}

// A step whose services fail to start leaves nothing on the host: its
// teardown removes every container labelled as the stack's, including
// one an earlier attempt left (an AppTown stopped mid-step), before the
// network, which Docker refuses to remove while anything is on it. A
// step's empty /16 network once stayed for weeks this way.
//
// Kills: a teardown that removes only the containers this attempt started.
func TestFailedServicesStartRemovesAnEarlierAttemptsContainerBeforeTheNetwork(t *testing.T) {
	t.Parallel()
	daemon := dockerfake.New()
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		cmd := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(cmd, "ps -aq --filter label=forge.class=service --filter label=forge.owner=817"):
			return []byte("left-by-earlier-attempt\n"), nil, true
		case strings.HasPrefix(cmd, "run ") && strings.Contains(cmd, "--name apptown-svc-817-db"):
			return []byte(`Conflict. The container name "/apptown-svc-817-db" is already in use`), errors.New("exit status 125"), true
		}
		return nil, nil, false
	})
	dk := docker.Scope{Client: daemon}
	if _, _, err := docker.StartServices(t.Context(), dk, "817", []deploy.ServiceSpec{{Name: "db", Image: "db:1"}}); err == nil {
		t.Fatal("a service that did not start reported started")
	}
	removedEarlier, removedNetwork := -1, -1
	for i, args := range daemon.Commands() {
		switch strings.Join(args, " ") {
		case "rm -f left-by-earlier-attempt":
			removedEarlier = i
		case "network rm apptown-net-817":
			removedNetwork = i
		}
	}
	if removedEarlier < 0 || removedNetwork < 0 || removedEarlier > removedNetwork {
		t.Fatalf("the earlier attempt's container (at %d) was not removed before the network (at %d): %v", removedEarlier, removedNetwork, daemon.Commands())
	}
}
