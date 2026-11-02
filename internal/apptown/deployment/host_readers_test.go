package deployment

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// Attachment wakes the manager immediately, including unreachable hosts. There
// is only one reader command in flight, and retirement cancels and joins it.
func TestHostReadersAttachAndRetireCancelTheOnlyCommand(t *testing.T) {
	t.Parallel()
	svc, db, _, _, _ := healthWorld(t)
	dir := shortHostDataDir(t)
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("HOST_KEY=-----BEGIN PRIVATE KEY-----\\nfixture\\n-----END PRIVATE KEY-----\n"), 0600); err != nil {
		t.Fatal(err)
	}
	secrets := secret.NewService(db, secret.Config{DataDir: dir})
	entered, cancelled := make(chan struct{}), make(chan struct{})
	var inFlight atomic.Int32
	var hosts *Hosts
	client := sshDockerFake(func(ctx context.Context, host string, cmd docker.Command) ([]byte, error) {
		if cmd.Args[0] == "foreground" {
			return []byte("foreground"), nil
		}
		hosts.connections.Range(func(_, v any) bool {
			if n := len(v.(*sshConnection).calls); n != 0 {
				t.Errorf("actual loop consumed %d foreground permits", n)
			}
			return true
		})
		if inFlight.Add(1) != 1 {
			t.Error("two loop commands in flight")
		}
		defer inFlight.Add(-1)
		if cmd.Args[0] != "info" {
			t.Errorf("command ran past blocked info: %v", cmd.Args)
		}
		close(entered)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	targets := NewTargets(db, nil)
	hosts = NewHosts(targets, secrets, HostSettings{Scope: docker.Scope{Client: client}, DataDir: dir, SSHConfigPath: filepath.Join(dir, "config"), SSHCommand: (&masterFake{alive: true}).run, NoLocalDocker: true})
	r := HostReaders{Snapshots: NewHostSnapshots(db, func() time.Time { return time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC) }), Targets: targets, Hosts: hosts, Providers: svc.providers, Inventory: svc.Inventory}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	defer func() { cancel(); <-done }()
	// Attach through the owner, rather than inserting a row around its wakeup.
	target, err := targets.attach(t.Context(), &Target{Name: "attached", Kind: KindDockerHost, Origin: OriginAttached, Status: TargetUnreachable, Address: map[string]any{"host": "ssh://deploy@box.example", "keyFrom": "HOST_KEY"}}, attachment{By: "system"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("attachment did not start reader")
	}
	scope, err := hosts.DockerScopeFor(t.Context(), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scope.Run(t.Context(), "foreground"); err != nil {
		t.Fatalf("foreground blocked by lap: %v", err)
	}
	// Repeated lifecycle notices must not make a second writer for this host.
	targets.notifyReaders()
	if err := targets.Retire(t.Context(), target.Name, "system"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("retirement did not cancel in-flight command")
	}
	cancel()
	<-done
	// A cancelled lap must not publish partial evidence after retirement.
	got, err := r.Snapshots.reading(t.Context(), target)
	if err != nil || !got.AttemptedAt.IsZero() {
		t.Fatalf("retired loop stored a reading: %+v %v", got, err)
	}
}
