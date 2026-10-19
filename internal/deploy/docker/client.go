// Package docker is the container runtime the forge drives: which daemon a
// command talks to (the forge's own, or an attached host's over SSH), the
// command itself, and the small questions asked of a daemon (is a
// container running, what is an image's digest, where is a port
// reachable). Every other driver and the console runtime build on it.
package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/strs"
)

// Client is the command transport consumed by a daemon scope. One client
// handles ordinary calls, bounded observations and streamed backup data.
// The composition root supplies it; a missing client never selects a real daemon.
type Client interface {
	Run(context.Context, string, Command) ([]byte, error)
}

type Command struct {
	Args    []string
	Stdin   io.Reader
	Stdout  io.Writer     // nil captures combined output; non-nil streams stdout only
	Stderr  io.Writer     // optional separate stream, e.g. the build log
	Timeout time.Duration // bounded observations retain the process-tree runner
	// Env adds NAME=value pairs to the docker CLI's own environment, so a
	// secret reaches a container by `-e NAME` and never appears in an
	// argument list another process can read.
	Env []string
}

type CLI struct{ Binary string }

// ErrNoLocalBackend refuses a call to the forge's own Docker on a forge
// started with -no-local-docker. It is a backstop: owners answer from the
// mode first (Scope.Absent), and never read it as "not running".
var ErrNoLocalBackend = errors.New("this AppTown has no local Docker")

// Refusing is the client of a forge with no local Docker daemon: every call
// is refused with ErrNoLocalBackend. An admitted target's scope (WithHost)
// gets Attached instead, which passes that target's --host on every call,
// so neither the ambient context nor DOCKER_HOST ever chooses a daemon.
type Refusing struct{ Attached Client }

func (Refusing) Run(context.Context, string, Command) ([]byte, error) { return nil, ErrNoLocalBackend }

// NoLocalBackend marks a client that stands for no daemon: Refusing, or a
// test's recording stand-in for it.
func (Refusing) NoLocalBackend() {}

// Absent reports the forge's own daemon on a forge that has none.
func (dk Scope) Absent() bool {
	_, ok := dk.Client.(interface{ NoLocalBackend() })
	return ok
}

func (c CLI) Run(ctx context.Context, host string, req Command) ([]byte, error) {
	binary := c.Binary
	if binary == "" {
		binary = "docker"
	}
	args := req.Args
	if host != "" {
		args = append([]string{"--host", host}, args...)
	}
	if req.Timeout > 0 {
		if req.Stdin != nil || req.Stdout != nil || req.Stderr != nil {
			return nil, errors.New("bounded Docker observation cannot stream data")
		}
		out := (execution.Runner{}).Run(ctx, execution.Local, execution.Request{Argv: append([]string{binary}, args...), Timeout: req.Timeout, Env: append([]string{builderEnv + "=", "BUILDX_CONFIG="}, req.Env...)})
		return out.Output, out.RunErr
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = append(withoutBuilder(os.Environ()), req.Env...)
	cmd.Stdin = req.Stdin
	if req.Stdout == nil && req.Stderr == nil {
		return cmd.CombinedOutput()
	}
	cmd.Stdout = req.Stdout
	if req.Stderr != nil {
		cmd.Stderr = req.Stderr
		return nil, cmd.Run()
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stderr.Bytes(), err
}

// builderEnv names a Buildx builder for every build the Docker CLI starts,
// even a plain `docker build`, which otherwise uses the daemon's own. The
// forge chooses the daemon (--host, or its own); a builder named in its
// environment would send a project's source and build to another machine,
// so no command the forge runs inherits it.
const builderEnv = "BUILDX_BUILDER"

// withoutBuilder also drops BUILDX_CONFIG, which would point buildx at an
// ambient store of builders: a boot's own Docker configuration keeps its
// own. Scope.Build names the daemon's builder either way.
func withoutBuilder(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, builderEnv+"=") && !strings.HasPrefix(kv, "BUILDX_CONFIG=") {
			out = append(out, kv)
		}
	}
	return out
}

func (dk Scope) WithHost(host, hostname, domain string) Scope {
	client := dk.Client
	if r, ok := client.(Refusing); ok && host != "" {
		client = r.Attached
	}
	return Scope{Client: client, Host: host, Hostname: hostname, Domain: domain, Now: dk.Now}
}

func (dk Scope) Command(ctx context.Context, req Command) ([]byte, error) {
	if dk.Client == nil {
		return nil, errors.New("Docker scope has no command client")
	}
	if dk.Lifetime != nil {
		if err := dk.Lifetime.Err(); err != nil {
			return nil, fmt.Errorf("the boot's time is up: %w", err)
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		defer context.AfterFunc(dk.Lifetime, cancel)()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if dk.Config != "" {
		// The boot's own Docker configuration (WithBootConfig): its login
		// is the boot's alone.
		req.Args = append([]string{"--config", dk.Config}, req.Args...)
	}
	return dk.Client.Run(ctx, dk.Host, req)
}

func (dk Scope) ToFile(ctx context.Context, path string, args ...string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	out, runErr := dk.Command(ctx, Command{Args: args, Stdout: f})
	closeErr := f.Close()
	if runErr != nil {
		return fmt.Errorf("%w: %s", runErr, strs.Truncate(strings.TrimSpace(string(out)), 300))
	}
	return closeErr
}

func (dk Scope) FromFile(ctx context.Context, path string, args ...string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return dk.Command(ctx, Command{Args: args, Stdin: f})
}
