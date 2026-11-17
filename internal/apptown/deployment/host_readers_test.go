package deployment

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
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

// The real per-target loop includes the lap's time in its interval. A slow lap
// is immediately followed by the next one, with no concurrent host command.
func TestHostReadersStartOnCadenceAndStoreTheSameDeadline(t *testing.T) {
	for _, took := range []time.Duration{16 * time.Second, 25 * time.Second} {
		t.Run(took.String(), func(t *testing.T) {
			svc, db, daemon, _, target := healthWorld(t)
			synctest.Test(t, func(t *testing.T) {
				// Connections opened by a lap own timers in this bubble; close them here.
				defer func() {
					if err := db.Close(); err != nil {
						t.Error(err)
					}
				}()
				snapshots := NewHostSnapshots(db, time.Now)
				reader := HostReaders{Snapshots: snapshots, Targets: NewTargets(db, nil), Hosts: svc.hosts, Providers: svc.providers, Inventory: svc.Inventory}
				ctx, cancel := context.WithCancel(t.Context())
				starts := make(chan time.Time, 2)
				count := 0
				daemon.SetReply(func(args []string) ([]byte, error, bool) {
					if args[0] == "info" {
						count++
						starts <- snapshots.now()
						if count == 1 {
							<-time.After(took)
						} else {
							<-ctx.Done()
							return nil, ctx.Err(), true
						}
					}
					return nil, nil, false
				})
				done := make(chan struct{})
				go func() { defer close(done); reader.Run(ctx) }()
				defer func() { cancel(); <-done }()
				first, second := <-starts, <-starts
				if got, want := second.Sub(first), max(hostReadInterval, took); got != want {
					t.Fatalf("next lap started after %s; want %s", got, want)
				}
				reading, err := snapshots.reading(t.Context(), target)
				if err != nil || !reading.NextAt.Equal(first.Add(hostReadInterval)) {
					t.Fatalf("stored deadline=%s, want %s: %v", reading.NextAt, first.Add(hostReadInterval), err)
				}
			})
		})
	}
}

// Slow sequential laps stay current through the next publication; an omitted
// lap expires without a running collector. Sweep evidence has its own clock.
func TestHostReadingDeadlineFollowsTwoSlowPublications(t *testing.T) {
	w := newDiagnosticsWorld(t)
	synctest.Test(t, func(t *testing.T) {
		defer func() {
			if err := w.s.db.Close(); err != nil {
				t.Error(err)
			}
		}()
		start, base := time.Now(), w.at
		w.s.setClock(func() time.Time { return base.Add(time.Since(start)) })
		snapshots := NewHostSnapshots(w.s.db, w.s.now)
		reader := HostReaders{Snapshots: snapshots, Targets: NewTargets(w.s.db, nil), Hosts: w.s.hosts, Providers: w.s.providers}
		if err := w.s.ObserveRuntime(t.Context()); err != nil {
			t.Fatal(err)
		}
		records, err := w.s.listRecords(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		count := 0
		starts := make(chan struct{}, 3)
		w.daemon.SetReply(func(args []string) ([]byte, error, bool) {
			if args[0] == "info" {
				count++
				starts <- struct{}{}
				if count > 2 {
					<-ctx.Done()
					return nil, ctx.Err(), true
				}
			}
			if args[0] == "ps" {
				<-time.After(2 * time.Second)
			}
			if args[0] == "image" && args[1] == "ls" {
				<-time.After(35 * time.Second)
			}
			return nil, nil, false
		})
		done := make(chan struct{})
		go func() { defer close(done); reader.Run(ctx) }()
		defer func() { cancel(); <-done }()
		<-starts
		<-time.After(37 * time.Second)
		synctest.Wait()
		<-starts
		first, err := snapshots.reading(t.Context(), w.remote)
		if err != nil || first.Took != 37*time.Second || !first.NextAt.Equal(base.Add(20*time.Second)) {
			t.Fatalf("first publication: %+v %v", first, err)
		}
		check := func(stale bool) {
			t.Helper()
			d, e := w.s.Diagnostics(t.Context(), w.project, "main")
			if e != nil || d.Reading.Stale != stale || d.Reading.Unknown != stale {
				t.Fatalf("diagnostics at %s: %+v %v", time.Since(start), d.Reading, e)
			}
			r, e := snapshots.reading(t.Context(), w.remote)
			if e != nil {
				t.Fatal(e)
			}
			inv, e := snapshots.inventory(r)
			if e != nil || inv.Stale != stale || inv.Unknown {
				t.Fatalf("inventory at %s: %+v %v", time.Since(start), inv, e)
			}
		}
		<-time.After(36 * time.Second)
		synctest.Wait()
		check(false)
		// Fresh host laps cannot extend a sweep's independently collected evidence.
		found := false
		for _, r := range records {
			if r.deployment.ID == w.main.ID {
				found = true
				v := w.s.observations.read(r, w.s.now())
				if !v.Stale || v.State != RuntimeUnknown {
					t.Fatalf("old sweep renewed by host: %+v", v)
				}
			}
		}
		if !found {
			t.Fatal("missing sweep deployment")
		}
		<-time.After(time.Second)
		synctest.Wait()
		<-starts
		check(false)
		second, e := snapshots.reading(t.Context(), w.remote)
		if e != nil || second.Took != 37*time.Second || !second.AttemptedAt.Equal(base.Add(37*time.Second)) {
			t.Fatalf("second publication: %+v %v", second, e)
		}
		// If the second publication had been omitted, the first expires at 84 s.
		<-time.After(10*time.Second + time.Nanosecond)
		synctest.Wait()
		if !first.stale(w.s.now(), first.Containers.ObservedAt) {
			t.Fatal("omitted second lap did not expire")
		}
		check(false)
		<-time.After(27 * time.Second)
		synctest.Wait()
		check(false)
		<-time.After(10 * time.Second)
		synctest.Wait()
		check(true)
	})
}

func TestFailedContainerLapKeepsItsEvidenceAge(t *testing.T) {
	t.Parallel()
	w := newDiagnosticsWorld(t)
	snapshots := NewHostSnapshots(w.s.db, w.s.now)
	reader := HostReaders{Snapshots: snapshots, Targets: NewTargets(w.s.db, nil), Hosts: w.s.hosts, Providers: w.s.providers}
	if err := reader.lap(t.Context(), w.remote.ID, w.s.now().Add(hostReadInterval)); err != nil {
		t.Fatal(err)
	}
	first, err := snapshots.reading(t.Context(), w.remote)
	if err != nil {
		t.Fatal(err)
	}
	w.advance(time.Minute)
	w.failContainerListing(context.DeadlineExceeded)
	if err := reader.lap(t.Context(), w.remote.ID, w.s.now().Add(hostReadInterval)); err != nil {
		t.Fatal(err)
	}
	r, err := snapshots.reading(t.Context(), w.remote)
	if err != nil || !r.Containers.ObservedAt.Equal(first.Containers.ObservedAt) {
		t.Fatalf("failed section renewed: %+v %v", r, err)
	}
	inv, err := snapshots.inventory(r)
	if err != nil || !inv.Stale || !inv.ObservedAt.Equal(first.Containers.ObservedAt) {
		t.Fatalf("inventory renewed: %+v %v", inv, err)
	}
	d, err := w.s.Diagnostics(t.Context(), w.project, "main")
	if err != nil || !d.Reading.Stale || !d.Reading.Unknown || !d.Reading.ObservedAt.Equal(first.Containers.ObservedAt) {
		t.Fatalf("diagnostics renewed: %+v %v", d.Reading, err)
	}
}
