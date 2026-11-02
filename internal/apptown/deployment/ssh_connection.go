package deployment

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/docker/cli/cli/connhelper"

	"github.com/flokkit/apptown/internal/deploy"
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

// sshDocker shares one authenticated connection and bounds actual SSH sessions,
// including the separate HTTP streams opened by one docker stats command.
type sshDocker struct {
	client                docker.Client
	run                   SSHCommand
	config, alias, socket string
	connection            *sshConnection
}

type sshConnection struct {
	mu       sync.Mutex
	calls    chan struct{}
	sessions chan struct{}
}

func (c sshDocker) Run(ctx context.Context, host string, cmd docker.Command) (out []byte, err error) {
	caller := ctx
	// Bound the whole command, including waiting for a call slot and its proxy.
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	if cmd.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cmd.Timeout)
		defer cancel()
		defer func() {
			if err != nil && ctx.Err() == context.DeadlineExceeded && caller.Err() == nil {
				err = deploy.Unreachable(err)
			}
		}()
	}
	// A call can retain several sessions while requesting another (attach,
	// BuildKit, stats). Admit at most three calls before taking session slots
	// so those retained streams leave room for each call to make progress.
	select {
	case c.connection.calls <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.connection.calls }()
	if err := c.ensure(ctx); err != nil {
		return []byte(err.Error()), err
	}
	cmd.Dial = func(dialCtx context.Context) (net.Conn, error) {
		return c.connection.open(ctx, func() (net.Conn, error) {
			helper, err := connhelper.GetConnectionHelperWithSSHOpts(host, []string{"-F", c.config, "-S", c.socket})
			if err != nil {
				return nil, err
			}
			return helper.Dialer(dialCtx, "", "")
		})
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
	connection, _ := h.connections.LoadOrStore(socket, &sshConnection{calls: make(chan struct{}, 3), sessions: make(chan struct{}, 10)})
	run := h.settings.SSHCommand
	if run == nil {
		run = runSSH
	}
	scope.Client = sshDocker{client: scope.Client, run: run, config: h.settings.SSHConfigPath,
		alias: strings.TrimPrefix(host, "ssh://"), socket: socket, connection: connection.(*sshConnection)}
	return scope
}

// Ten is OpenSSH's default MaxSessions. The master uses no session itself.
// A permit stays held until the dial-stdio process has been closed and reaped.
func (c *sshConnection) open(ctx context.Context, dial func() (net.Conn, error)) (net.Conn, error) {
	select {
	case c.sessions <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-c.sessions
		return nil, err
	}
	conn, err := dial()
	if err != nil {
		<-c.sessions
		return nil, err
	}
	owned := &sshSession{Conn: conn, release: func() { <-c.sessions }}
	owned.stop = context.AfterFunc(ctx, func() { owned.close() })
	return owned, nil
}

type sshSession struct {
	net.Conn
	once    sync.Once
	release func()
	stop    func() bool
	err     error
}

func (s *sshSession) Close() error {
	s.stop()
	return s.close()
}
func (s *sshSession) close() error {
	s.once.Do(func() { s.err = s.Conn.Close(); s.release() })
	return s.err
}

// Preserve stdin EOF for upgraded Docker attach/exec streams.
func (s *sshSession) CloseWrite() error {
	if conn, ok := s.Conn.(interface{ CloseWrite() error }); ok {
		return conn.CloseWrite()
	}
	return fmt.Errorf("SSH session cannot close its write side: %w", errors.ErrUnsupported)
}
