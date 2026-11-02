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

	"github.com/flokkit/apptown/internal/deploy/docker"
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
	if err == nil || !strings.Contains(string(out), "Connection closed by UNKNOWN") {
		t.Fatalf("direct connection fallback was not refused: %v: %s", err, out)
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenSSH kept a dead master's socket: %v", err)
	}
}

func TestSSHCallsWaitAtThreePerTargetAndCanBeCancelled(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		started, release := make(chan struct{}, 3), make(chan struct{})
		t.Cleanup(func() { close(release) })
		client := sshDockerFake(func(ctx context.Context, _ string, _ docker.Command) ([]byte, error) {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil, nil
		})
		master := &masterFake{alive: true}
		h := NewHosts(nil, nil, HostSettings{Scope: docker.Scope{Client: client}, SSHCommand: master.run})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var wg sync.WaitGroup
		t.Cleanup(wg.Wait)
		for range 3 {
			wg.Go(func() { _, _ = h.attachedScope("ssh://box", "", "", "/socket").Run(ctx, "events") })
		}
		for range 3 {
			<-started
		}
		waiting, stop := context.WithCancel(t.Context())
		defer stop()
		result := make(chan error, 1)
		go func() { _, err := h.attachedScope("ssh://box", "", "", "/socket").Run(waiting, "info"); result <- err }()
		synctest.Wait()
		if len(started) != 0 {
			t.Error("a fourth Docker call ran while three calls were active")
		}
		stop()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting Docker call ignored cancellation: %v", err)
		}
		master.mu.Lock()
		checks := master.checks
		master.mu.Unlock()
		if checks != 3 {
			t.Fatalf("a fourth call reached SSH while three calls were active: %d checks", checks)
		}
		cancel()
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
