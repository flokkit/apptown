package apptown

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

type agentImageDocker struct{ calls []string }

func (c *agentImageDocker) Run(_ context.Context, host string, req docker.Command) ([]byte, error) {
	c.calls = append(c.calls, strings.TrimSpace(host+" "+strings.Join(req.Args, " ")))
	return nil, nil
}

// The agent image's build is given the extra certificates as its secret: its
// RUN lines download (deploy/apptown-agent/Dockerfile).
// Not parallel: it installs the process's trust.
func TestTheAgentImageBuildIsGivenTheExtraCertificates(t *testing.T) {
	trusttest.Install(t, trusttest.New(t, "Company CA").Bundle(t), trust.Bundle{})
	client := &agentImageDocker{}
	if err := fetchAgentImage(context.Background(), docker.Scope{Client: client}, "apptown-agent", "/ctx", false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 1 || client.calls[0] != "build "+strings.Join(trust.BuildArgs(true), " ")+" -t apptown-agent /ctx" ||
		!strings.Contains(client.calls[0], "--secret id=apptown-trust,env=APPTOWN_TRUST ") {
		t.Fatalf("calls %q", client.calls)
	}
}
