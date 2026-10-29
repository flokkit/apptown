package apptown

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

type agentImageDocker struct{ calls []string }

func (c *agentImageDocker) Run(_ context.Context, host string, req docker.Command) ([]byte, error) {
	// The build rule's questions (docker.Scope.Build): buildx is there and
	// the context's builder is the daemon's own.
	switch strings.Join(req.Args, " ") {
	case "buildx version":
		return []byte("github.com/docker/buildx v0.38.1"), nil
	case "context show":
		return []byte("default\n"), nil
	case "buildx inspect default":
		return []byte("Name: default\nDriver: docker\n"), nil
	}
	c.calls = append(c.calls, strings.TrimSpace(host+" "+strings.Join(req.Args, " ")))
	return nil, nil
}

// The agent image is built by the one build rule (docker.Scope.Build): the
// daemon's own builder named, the extra certificates as its secret (its RUN
// lines download, deploy/apptown-agent/Dockerfile) and AppTown's build
// label, for the maintenance sweep's prune.
// Not parallel: it installs the process's trust.
func TestTheAgentImageBuildIsGivenTheExtraCertificates(t *testing.T) {
	trusttest.Install(t, trusttest.New(t, "Company CA").Bundle(t), trust.Bundle{})
	client := &agentImageDocker{}
	if err := fetchAgentImage(context.Background(), docker.Scope{Client: client}, "apptown-agent", "/ctx", false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 1 || client.calls[0] != "build "+strings.Join(trust.BuildArgs(true), " ")+" "+strings.Join(dockerlabel.BuiltArgs(), " ")+" --builder default -t apptown-agent /ctx" ||
		!strings.Contains(client.calls[0], "--secret id=apptown-trust,env=APPTOWN_TRUST ") {
		t.Fatalf("calls %q", client.calls)
	}
}
