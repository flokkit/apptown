package docker

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
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
		if err := LoginRegistry(context.Background(), scope, "registry.example", "robot", "private-canary", image); err != nil {
			t.Fatal(err)
		}
	}
	if c.calls != 0 {
		t.Fatal("authenticated an unrelated image")
	}
	if err := LoginRegistry(context.Background(), scope, "registry.example", "robot", "private-canary", "registry.example/team/app@sha256:abc"); err != nil {
		t.Fatal(err)
	}
	if c.calls != 1 || c.host != "ssh://remote" || c.password != "private-canary" || strings.Contains(strings.Join(c.args, " "), "private-canary") {
		t.Fatal("registry authentication scope or credential channel changed")
	}
}

// A registry login that fails is the error, and the image is not pulled:
// a pull after it could succeed with another credential Docker holds.
//
// Kills: the login's error dropped before the pull.
func TestAFailedRegistryLoginPullsNothing(t *testing.T) {
	t.Parallel()
	fake := &fakeClient{}
	fake.reply = func(req Command) ([]byte, error) {
		if req.Args[0] == "login" {
			return []byte("unauthorized: incorrect username or password"), errors.New("exit status 1")
		}
		return nil, nil
	}
	dk := Scope{Client: fake}
	err := LoginRegistry(t.Context(), dk, "registry.example", "robot", "wrong-token", "registry.example/team/app:1")
	if err == nil || !strings.Contains(err.Error(), "unauthorized") || strings.Contains(err.Error(), "wrong-token") {
		t.Fatalf("a failed login: %v", err)
	}
	host := NewHost(Dependencies{StaticServer: testStaticServer, DFImage: testDF, Registry: registryLoginFunc(func(ctx context.Context, dk Scope, image string) error {
		return LoginRegistry(ctx, dk, "registry.example", "robot", "wrong-token", image)
	})})
	plan := appcontract.Prepare(&appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Image: "registry.example/team/app:1", Port: 8080}}}, nil)
	if _, err := host.PrepareImages(t.Context(), BootRequest{Name: "apptown-env-p-main", Docker: dk}, plan); err == nil {
		t.Fatal("the image was prepared after a failed login")
	}
	for _, args := range fake.args {
		if args[0] == "pull" {
			t.Fatalf("pulled after a failed login: %v", fake.args)
		}
	}
}

type registryLoginFunc func(ctx context.Context, dk Scope, image string) error

func (f registryLoginFunc) Login(ctx context.Context, dk Scope, image string) error {
	return f(ctx, dk, image)
}
