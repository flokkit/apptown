package docker

import (
	"context"
	"io"
	"strings"
	"testing"
)

type registryClient struct {
	calls    int
	host     string
	args     []string
	password string
}

func (c *registryClient) Run(_ context.Context, host string, in Command) ([]byte, error) {
	c.calls++
	c.host = host
	c.args = in.Args
	raw, err := io.ReadAll(in.Stdin)
	c.password = string(raw)
	return nil, err
}

func TestRegistryLoginMatchesImageHostAndKeepsTokenOffArguments(t *testing.T) {
	t.Parallel()
	c := &registryClient{}
	scope := Scope{Host: "ssh://remote", Client: c}
	for _, image := range []string{"alpine:latest", "registry.example.evil/app", "evil/registry.example/app"} {
		LoginRegistry(context.Background(), scope, "registry.example", "robot", "private-canary", image)
	}
	if c.calls != 0 {
		t.Fatal("authenticated an unrelated image")
	}
	LoginRegistry(context.Background(), scope, "registry.example", "robot", "private-canary", "registry.example/team/app@sha256:abc")
	if c.calls != 1 || c.host != "ssh://remote" || c.password != "private-canary" || strings.Contains(strings.Join(c.args, " "), "private-canary") {
		t.Fatal("registry authentication scope or credential channel changed")
	}
}
