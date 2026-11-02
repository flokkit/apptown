package deployment

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"time"
)

type masterFake struct {
	mu          sync.Mutex
	alive       bool
	unavailable bool
	starts      int
	checks      int
}

func (f *masterFake) run(_ context.Context, args []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if slices.Contains(args, "check") {
		f.checks++
		if !f.alive {
			return nil, errors.New("no master")
		}
		return nil, nil
	}
	if !slices.Contains(args, "-Nf") || !slices.Contains(args, "ControlMaster=auto") || !slices.Contains(args, "ProxyCommand=none") || !slices.Contains(args, "BatchMode=yes") {
		return nil, errors.New("unexpected master command")
	}
	f.starts++
	f.alive = !f.unavailable
	return nil, nil
}

type sshDockerFake func(context.Context, string, docker.Command) ([]byte, error)

func (f sshDockerFake) Run(ctx context.Context, host string, cmd docker.Command) ([]byte, error) {
	return f(ctx, host, cmd)
}

// A burst must create one master, a stream must leave room for short calls,
// and the same retained scope must replace a killed master on its next call.
func TestSSHMasterIsSharedAndReplacedAfterDeath(t *testing.T) {
	t.Parallel()
	master := &masterFake{}
	streamStarted, endStream := make(chan struct{}), make(chan struct{})
	client := sshDockerFake(func(_ context.Context, host string, cmd docker.Command) ([]byte, error) {
		if cmd.Dial == nil {
			t.Error("Docker call bypasses session transport")
		}
		if host != "ssh://apptown-target-box" {
			t.Errorf("wrong host %q", host)
		}
		if slices.Contains(cmd.Args, "events") {
			close(streamStarted)
			<-endStream
		}
		return []byte("reading"), nil
	})
	h := NewHosts(nil, nil, HostSettings{Scope: docker.Scope{Client: client}, SSHConfigPath: "/config", SSHCommand: master.run})
	call := func() {
		// Independent scopes must share the same establishment lock.
		scope := h.attachedScope("ssh://apptown-target-box", "", "", "/socket")
		if _, err := scope.Run(t.Context(), "info"); err != nil {
			t.Error(err)
		}
	}
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(call)
	}
	wg.Wait()
	master.mu.Lock()
	if master.starts != 1 {
		t.Errorf("burst opened %d masters", master.starts)
	}
	master.mu.Unlock()
	scope := h.attachedScope("ssh://apptown-target-box", "", "", "/socket")
	wg.Go(func() {
		if _, err := scope.Run(t.Context(), "events"); err != nil {
			t.Error(err)
		}
	})
	<-streamStarted
	call()
	close(endStream)
	wg.Wait()
	master.mu.Lock()
	master.alive = false // ssh process killed, host reboot or dropped transport
	master.mu.Unlock()
	if _, err := scope.Run(t.Context(), "stats"); err != nil {
		t.Fatal(err)
	}
	master.mu.Lock()
	defer master.mu.Unlock()
	if master.starts != 2 {
		t.Fatalf("dead master was not replaced: %d starts", master.starts)
	}
}

func TestSSHSharingUnavailableRefusesDocker(t *testing.T) {
	t.Parallel()
	master := &masterFake{unavailable: true}
	client := sshDockerFake(func(context.Context, string, docker.Command) ([]byte, error) {
		t.Error("Docker ran without a working SSH master")
		return nil, nil
	})
	h := NewHosts(nil, nil, HostSettings{Scope: docker.Scope{Client: client}, SSHConfigPath: "/config", SSHCommand: master.run})
	_, err := h.attachedScope("ssh://box", "", "", "/socket").Run(t.Context(), "info")
	if err == nil || !strings.Contains(err.Error(), "SSH connection sharing for box is unavailable") {
		t.Fatalf("missing sharing failure: %v", err)
	}
}

// Parse with the real client without opening a network connection. In
// particular, ProxyCommand must survive ssh_config's quoting and token rules.
func TestOpenSSHReadsSharingAndRefusesDirectFallback(t *testing.T) {
	t.Parallel()
	dir := shortHostDataDir(t)
	socket := filepath.Join(dir, "socket")
	alias := sshAlias(sshTargetName("box", socket))
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte("Host *\n ControlMaster no\n ControlPath /wrong\n ProxyCommand wrong\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := upsertSSHStanza(cfg, "box", socket, sshStanza("box", sshAddress{User: "deploy", Host: "box.example", Port: 22}, "/key", socket)); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := upsertSSHStanza(cfg, "box", socket, sshStanza("box", sshAddress{User: "deploy", Host: "box.example", Port: 22}, "/key", socket)); err != nil {
		t.Fatal(err)
	}
	if after, err := os.Stat(cfg); err != nil || !os.SameFile(before, after) {
		t.Fatalf("unchanged config with user defaults was rewritten: %v", err)
	}
	out, err := runSSH(t.Context(), []string{"-G", "-F", cfg, "--", alias})
	if err != nil {
		t.Fatalf("OpenSSH refused the config: %v: %s", err, out)
	}
	for _, option := range []string{"controlmaster auto\n", "controlpersist 180\n", "serveraliveinterval 15\n", "serveralivecountmax 2\n", "controlpath " + socket + "\n", "proxycommand false\n"} {
		if !strings.Contains(string(out), option) {
			t.Fatalf("OpenSSH did not read %q:\n%s", option, out)
		}
	}
	// A killed master can leave a socket inode behind. The real OpenSSH
	// client must discard it, and the fallback must still be refused.
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	out, err = runSSH(t.Context(), []string{"-F", cfg, "--", alias, "true"})
	// ProxyCommand false refuses the direct connection: OpenSSH then reports
	// its stand-in peer, UNKNOWN, as closing ("Connection closed by UNKNOWN")
	// or breaking ("Connection to UNKNOWN port 65535: Broken pipe"),
	// depending on its version. Either way no host was reached.
	if err == nil || !strings.Contains(string(out), "UNKNOWN port 65535") {
		t.Fatalf("direct connection fallback was not refused: %v: %s", err, out)
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenSSH kept a dead master's socket: %v", err)
	}
}

// Permits count individual sessions, not CLI invocations. Queued requests must
// cancel without opening a session, and closing one must let the queue advance.
func TestSSHSessionsWaitAtTenAndCanBeCancelled(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := NewHosts(nil, nil, HostSettings{})
		c := h.attachedScope("ssh://box", "", "", "/socket").Client.(sshDocker).connection
		dial := func() (net.Conn, error) { a, b := net.Pipe(); t.Cleanup(func() { b.Close() }); return a, nil }
		var conns []net.Conn
		for range 10 {
			conn, err := c.open(t.Context(), dial)
			if err != nil {
				t.Fatal(err)
			}
			conns = append(conns, conn)
			t.Cleanup(func() { conn.Close() })
		}
		waiting, stop := context.WithCancel(t.Context())
		result := make(chan error, 1)
		go func() {
			_, err := c.open(waiting, func() (net.Conn, error) { t.Error("eleventh session opened"); return dial() })
			result <- err
		}()
		synctest.Wait()
		stop()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting session: %v", err)
		}
		conns[0].Close()
		conn, err := c.open(t.Context(), dial)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		if len(c.sessions) != 9 {
			t.Fatalf("permits leaked: %d", len(c.sessions))
		}
	})
}

// Prepending an AppTown alias must preserve defaults written before any Host.
func TestOpenSSHPreservesTopLevelDefaults(t *testing.T) {
	t.Parallel()
	dir := shortHostDataDir(t)
	cfg, socket := filepath.Join(dir, "config"), filepath.Join(dir, "socket")
	original := "User personal\nForwardAgent yes\nHost unrelated\n  HostName unrelated.example\n"
	if err := os.WriteFile(cfg, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := upsertSSHStanza(cfg, "box", socket, sshStanza("box", sshAddress{User: "deploy", Host: "box.example", Port: 22}, "/key", socket)); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"unrelated", "another", sshAlias(sshTargetName("box", socket))} {
		out, err := runSSH(t.Context(), []string{"-G", "-F", cfg, "--", host})
		if err != nil {
			t.Fatalf("OpenSSH refused %s: %v: %s", host, err, out)
		}
		user := "personal"
		if host == sshAlias(sshTargetName("box", socket)) {
			user = "deploy"
		}
		for _, option := range []string{"user " + user + "\n", "forwardagent yes\n"} {
			if !strings.Contains(string(out), option) {
				t.Errorf("%s lost %q:\n%s", host, option, out)
			}
		}
	}
}

// Queueing is part of the observation deadline, and the owner still classifies
// that deadline as unreachable rather than caller cancellation.
func TestSSHObservationDeadlineIncludesTransportAndKeepsClassification(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		master := &masterFake{alive: true}
		client := sshDockerFake(func(ctx context.Context, _ string, _ docker.Command) ([]byte, error) {
			if _, ok := ctx.Deadline(); !ok {
				return nil, errors.New("observation has no deadline")
			}
			<-ctx.Done()
			return nil, ctx.Err()
		})
		h := NewHosts(nil, nil, HostSettings{Scope: docker.Scope{Client: client}, SSHCommand: master.run})
		_, err := h.attachedScope("ssh://box", "", "", "/socket").Command(t.Context(), docker.Command{Args: []string{"inspect", "app"}, Timeout: time.Second})
		if !deploy.IsUnreachable(err) {
			t.Fatalf("observation deadline lost classification: %v", err)
		}
	})
}

func TestSSHSessionCancellationClosesTransportAndReleasesPermit(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		c := &sshConnection{sessions: make(chan struct{}, 10)}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		local, remote := net.Pipe()
		defer remote.Close()
		conn, err := c.open(ctx, func() (net.Conn, error) { return local, nil })
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		synctest.Wait()
		if len(c.sessions) != 0 {
			t.Fatal("cancelled session kept its permit")
		}
		if _, err := remote.Read(make([]byte, 1)); err == nil {
			t.Fatal("cancelled session stayed open")
		}
		conn.Close() // transport cleanup can race cancellation; it releases once.
		_, err = c.open(t.Context(), func() (net.Conn, error) { return nil, errors.New("dial failed") })
		if err == nil || len(c.sessions) != 0 {
			t.Fatalf("failed dial leaked a permit: %v", err)
		}
	})
}

// Three calls can each retain two streams and still acquire a third. Further
// calls must wait before taking any sessions, including across retained scopes.
func TestSSHCallsLeaveRoomForEachOthersConnections(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		master := &masterFake{alive: true}
		release := make(chan struct{})
		var mu sync.Mutex
		started := 0
		var connection *sshConnection
		client := sshDockerFake(func(ctx context.Context, _ string, _ docker.Command) ([]byte, error) {
			mu.Lock()
			started++
			mu.Unlock()
			var conns []net.Conn
			defer func() {
				for _, conn := range conns {
					conn.Close()
				}
			}()
			// Use the production session limiter with in-memory transports.
			for i := range 3 {
				if i == 2 {
					<-release
				}
				conn, err := connection.open(ctx, func() (net.Conn, error) {
					a, b := net.Pipe()
					t.Cleanup(func() { b.Close() })
					return a, nil
				})
				if err != nil {
					return nil, err
				}
				conns = append(conns, conn)
			}
			return nil, nil
		})
		h := NewHosts(nil, nil, HostSettings{Scope: docker.Scope{Client: client}, SSHCommand: master.run})
		connection = h.attachedScope("ssh://box", "", "", "/socket").Client.(sshDocker).connection
		results := make(chan error, 6)
		call := func() {
			scope := h.attachedScope("ssh://box", "", "", "/socket")
			_, err := scope.Command(t.Context(), docker.Command{Args: []string{"run"}, Timeout: time.Second})
			results <- err
		}
		for range 3 {
			go call()
		}
		synctest.Wait()
		for range 3 {
			go call()
		}
		synctest.Wait()
		mu.Lock()
		n := started
		mu.Unlock()
		if n != 3 {
			t.Errorf("started %d calls while three held sessions; want 3", n)
		}
		close(release)
		for range 6 {
			if err := <-results; err != nil {
				t.Errorf("call did not finish: %v", err)
			}
		}
	})
}

func TestSSHMasterCheckedOncePerCommand(t *testing.T) {
	t.Parallel()
	master := &masterFake{alive: true}
	client := sshDockerFake(func(ctx context.Context, _ string, cmd docker.Command) ([]byte, error) {
		for range 2 {
			// Invalid SSH ports fail helper construction without starting a process.
			if _, err := cmd.Dial(ctx); err == nil {
				t.Error("invalid SSH port accepted")
			}
		}
		return nil, nil
	})
	h := NewHosts(nil, nil, HostSettings{Scope: docker.Scope{Client: client}, SSHCommand: master.run})
	if _, err := h.attachedScope("ssh://box:invalid", "", "", "/socket").Run(t.Context(), "info"); err != nil {
		t.Fatal(err)
	}
	if master.checks != 1 {
		t.Fatalf("master checked %d times for one command", master.checks)
	}
}

func TestSSHSessionReportsUnsupportedHalfClose(t *testing.T) {
	t.Parallel()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	s := &sshSession{Conn: a}
	if err := s.CloseWrite(); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("half-close without transport support: %v", err)
	}
}

func TestSSHCallQueueDeadlineDoesNotRunDockerOrLeakPermit(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		master := &masterFake{alive: true}
		runs := 0
		client := sshDockerFake(func(context.Context, string, docker.Command) ([]byte, error) {
			runs++
			return nil, errors.New("command failed")
		})
		h := NewHosts(nil, nil, HostSettings{Scope: docker.Scope{Client: client}, SSHCommand: master.run})
		scope := h.attachedScope("ssh://box", "", "", "/socket")
		c := scope.Client.(sshDocker).connection
		for range 3 {
			c.calls <- struct{}{}
		}
		_, err := scope.Command(t.Context(), docker.Command{Args: []string{"inspect", "app"}, Timeout: time.Second})
		if !deploy.IsUnreachable(err) || runs != 0 || master.checks != 0 {
			t.Fatalf("queued call: error %v, Docker runs %d, master checks %d", err, runs, master.checks)
		}
		for range 3 {
			<-c.calls
		}
		if _, err := scope.Run(t.Context(), "info"); err == nil || runs != 1 || len(c.calls) != 0 {
			t.Fatalf("failed call leaked admission: error %v, Docker runs %d, permits %d", err, runs, len(c.calls))
		}
	})
}
