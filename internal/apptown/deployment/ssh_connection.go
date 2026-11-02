package deployment

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
)

// SSHCommand runs the local OpenSSH client. Nil uses the bounded process runner.
// Construction can supply a transport fake without changing global executables.
type SSHCommand func(context.Context, []string) ([]byte, error)

func runSSH(ctx context.Context, args []string) ([]byte, error) {
	out := (execution.Runner{}).Run(ctx, execution.Local, execution.Request{
		Argv: append([]string{"ssh"}, args...), Timeout: 45 * time.Second,
	})
	return out.Output, out.RunErr
}

// sshDocker serializes connection establishment and bounds active Docker calls,
// including streams, to three per target. Each Docker CLI call can hold two or
// three SSH sessions for HTTP connections (run, attach and BuildKit), so eight
// calls can exceed sshd's default MaxSessions of ten. It checks on every call:
// a scope kept by a stream controller must recover without being reconstructed.
type sshDocker struct {
	client                docker.Client
	run                   SSHCommand
	config, alias, socket string
	connection            *sshConnection
}

type sshConnection struct {
	mu    sync.Mutex
	calls chan struct{}
}

func (c sshDocker) Run(ctx context.Context, host string, cmd docker.Command) ([]byte, error) {
	select {
	case c.connection.calls <- struct{}{}:
		defer func() { <-c.connection.calls }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := c.ensure(ctx); err != nil {
		return []byte(err.Error()), err
	}
	return c.client.Run(ctx, host, cmd)
}

func (c sshDocker) ensure(ctx context.Context) error {
	c.connection.mu.Lock()
	defer c.connection.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	args := []string{"-F", c.config, "-S", c.socket}
	check := append(append([]string{}, args...), "-O", "check", "--", c.alias)
	if _, err := c.run(ctx, check); err == nil {
		return nil
	}
	// Only this operation may make a TCP connection. The generated stanza's
	// ProxyCommand refuses an ordinary ssh client's direct-connection fallback.
	start := append(append([]string{}, args...), "-o", "ProxyCommand=none", "-o", "BatchMode=yes", "-o", "ControlMaster=auto", "-Nf", "--", c.alias)
	out, err := c.run(ctx, start)
	if err == nil {
		out, err = c.run(ctx, check)
	}
	if err != nil {
		return fmt.Errorf("SSH connection sharing for %s is unavailable: %w: %s", c.alias, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (h *Hosts) attachedScope(host, hostname, domain, socket string) docker.Scope {
	scope := h.settings.Scope.WithHost(host, hostname, domain)
	connection, _ := h.connections.LoadOrStore(socket, &sshConnection{calls: make(chan struct{}, 3)})
	run := h.settings.SSHCommand
	if run == nil {
		run = runSSH
	}
	scope.Client = sshDocker{client: scope.Client, run: run, config: h.settings.SSHConfigPath,
		alias: strings.TrimPrefix(host, "ssh://"), socket: socket, connection: connection.(*sshConnection)}
	return scope
}
