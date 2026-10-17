// Package executiontest is the conformance suite every TaskDriver passes. Its
// groups follow capabilities: the lifecycle group applies to every driver;
// the others run only for a driver that claims the capability. A fake driver
// passing the suite establishes contract behaviour, not backend support.
package executiontest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/poll"
)

// Harness adapts the suite to one driver. Spec returns a task that prints
// output, then either exits with exit or, when block is true, runs until
// stopped.
type Harness struct {
	Driver execution.TaskDriver
	Spec   func(key, output string, exit int, block bool) execution.TaskSpec
	// Key returns a key unique to this run of the suite.
	Key func(name string) string
}

// Run runs every group that applies to the harness's driver.
func Run(t *testing.T, h Harness) {
	t.Run("lifecycle", func(t *testing.T) { Lifecycle(t, h) })
	caps := h.Driver.Capabilities()
	for _, group := range []struct {
		name    string
		claimed bool
		run     func(*testing.T, Harness)
	}{{"private docker", caps.PrivateDocker, PrivateDocker}, {"terminal", caps.Terminal, nil}, {"persistence", caps.Persistence, nil}} {
		t.Run(group.name, func(t *testing.T) {
			if !group.claimed {
				t.Skipf("%T does not claim %s; admission refuses work that needs it", h.Driver, group.name)
			}
			if group.run == nil {
				t.Fatalf("%T claims %s, which has no conformance group yet", h.Driver, group.name)
			}
			group.run(t, h)
		})
	}
}

// PrivateDocker is what a driver claiming private Docker refuses before
// creating anything: a limit left at zero, which is never unlimited, and
// services it could not tell apart from each other or from the daemon.
// What it runs is established by its backend's integration tests
// (internal/worker, private_integration_test.go).
func PrivateDocker(t *testing.T, h Harness) {
	ctx := context.Background()
	limits := execution.Limits{CPUs: 1, MemoryBytes: 1 << 30, PIDs: 256}
	for name, change := range map[string]func(*execution.TaskSpec){
		"a zero limit":       func(s *execution.TaskSpec) { s.Admission.Limits.PIDs = 0 },
		"a service dind":     func(s *execution.TaskSpec) { s.Private.Services[0].Name = "dind" },
		"a duplicate":        func(s *execution.TaskSpec) { s.Private.Services[1].Name = "db" },
		"no network":         func(s *execution.TaskSpec) { s.Admission.Network = execution.NetworkNone },
		"a nameless service": func(s *execution.TaskSpec) { s.Private.Services[0].Name = "" },
	} {
		key := h.Key("private")
		spec := h.Spec(key, "hello", 0, false)
		spec.Admission.Limits, spec.Admission.Network = limits, execution.NetworkDefault
		spec.Private = &execution.PrivateDocker{Daemon: true, Services: []deploy.ServiceSpec{{Name: "db", Image: "postgres"}, {Name: "cache", Image: "redis"}}}
		change(&spec)
		if _, err := h.Driver.Ensure(ctx, spec); err == nil {
			t.Errorf("%s was not refused", name)
		}
		if obs, err := h.Driver.Observe(ctx, execution.TaskRef{Key: key}); err != nil || obs.State != execution.TaskAbsent {
			t.Errorf("refusing %s created %+v (%v)", name, obs, err)
		}
	}
}

// Lifecycle is what every driver provides: identity by key, idempotent
// creation, start, wait, output by cursor, stop, release, and refusal of
// requests it cannot honour.
func Lifecycle(t *testing.T, h Harness) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d := h.Driver

	t.Run("one key is one task", func(t *testing.T) {
		key := h.Key("identity")
		t.Cleanup(func() { _ = d.Release(context.Background(), execution.TaskRef{Key: key}) })
		if absent, err := d.Observe(ctx, execution.TaskRef{Key: key}); err != nil || absent.State != execution.TaskAbsent {
			t.Fatalf("before ensure: %+v %v", absent, err)
		}
		created, err := d.Ensure(ctx, h.Spec(key, "hello", 3, false))
		if err != nil || created.State != execution.TaskCreated || created.ID == "" {
			t.Fatalf("ensure: %+v %v", created, err)
		}
		again, err := d.Ensure(ctx, h.Spec(key, "hello", 3, false))
		if err != nil || again.ID != created.ID {
			t.Fatalf("a retried ensure made %+v, want the same task %s: %v", again, created.ID, err)
		}
		byKey, err := d.Observe(ctx, execution.TaskRef{Key: key})
		if err != nil || byKey.ID != created.ID || byKey.State != execution.TaskCreated {
			t.Fatalf("found by key before start: %+v %v", byKey, err)
		}
		if want := h.Spec(key, "hello", 3, false).Digest(); byKey.Request != want {
			t.Fatalf("the task reports request %q, want %q: a caller cannot tell it is its own", byKey.Request, want)
		}
	})

	t.Run("a task runs once and keeps its result until released", func(t *testing.T) {
		key := h.Key("result")
		t.Cleanup(func() { _ = d.Release(context.Background(), execution.TaskRef{Key: key}) })
		created, err := d.Ensure(ctx, h.Spec(key, "hello world", 3, false))
		if err != nil {
			t.Fatal(err)
		}
		ref := execution.TaskRef{Key: key, ProviderID: created.ID}
		if err := d.Start(ctx, ref); err != nil {
			t.Fatal(err)
		}
		code, err := d.Wait(ctx, ref)
		if err != nil || code != 3 {
			t.Fatalf("exit %d %v, want 3", code, err)
		}
		exited, err := d.Observe(ctx, ref)
		if err != nil || exited.State != execution.TaskExited || exited.ExitCode != 3 {
			t.Fatalf("after exit: %+v %v", exited, err)
		}
		first, err := d.Output(ctx, ref, execution.OutputRead{Cursor: 0, Max: 5})
		if err != nil || string(first.Data) != "hello" || first.Next != 5 {
			t.Fatalf("first page: %+v %v", first, err)
		}
		rest, err := d.Output(ctx, ref, execution.OutputRead{Cursor: first.Next})
		if err != nil || !strings.HasPrefix(string(rest.Data), " world") {
			t.Fatalf("next page: %q %v", rest.Data, err)
		}
		if err := d.Release(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if gone, err := d.Observe(ctx, ref); err != nil || gone.State != execution.TaskAbsent {
			t.Fatalf("after release: %+v %v", gone, err)
		}
	})

	t.Run("a second start does not run a task again", func(t *testing.T) {
		key := h.Key("once")
		t.Cleanup(func() { _ = d.Release(context.Background(), execution.TaskRef{Key: key}) })
		created, err := d.Ensure(ctx, h.Spec(key, "ran", 0, false))
		if err != nil {
			t.Fatal(err)
		}
		ref := execution.TaskRef{Key: key, ProviderID: created.ID}
		for range 2 {
			// A caller that lost Start's reply starts it again.
			if err := d.Start(ctx, ref); err != nil {
				t.Fatal(err)
			}
			if code, err := d.Wait(ctx, ref); err != nil || code != 0 {
				t.Fatalf("exit %d %v", code, err)
			}
		}
		page, err := d.Output(ctx, ref, execution.OutputRead{})
		if err != nil || strings.Count(string(page.Data), "ran") != 1 {
			t.Fatalf("the task ran again: output %q %v", page.Data, err)
		}
	})

	t.Run("overlapping starts run a task once", func(t *testing.T) {
		key := h.Key("overlap")
		t.Cleanup(func() { _ = d.Release(context.Background(), execution.TaskRef{Key: key}) })
		created, err := d.Ensure(ctx, h.Spec(key, "ran", 0, false))
		if err != nil {
			t.Fatal(err)
		}
		ref := execution.TaskRef{Key: key, ProviderID: created.ID}
		errs := make(chan error, 4)
		for range 4 {
			go func() { errs <- d.Start(ctx, ref) }()
		}
		for range 4 {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		for range 2 {
			if code, err := d.Wait(ctx, ref); err != nil || code != 0 {
				t.Fatalf("exit %d %v", code, err)
			}
		}
		page, err := d.Output(ctx, ref, execution.OutputRead{})
		if err != nil || strings.Count(string(page.Data), "ran") != 1 {
			t.Fatalf("overlapping starts ran the task more than once: output %q %v", page.Data, err)
		}
	})

	t.Run("a key is not adopted for a different request", func(t *testing.T) {
		key := h.Key("conflict")
		t.Cleanup(func() { _ = d.Release(context.Background(), execution.TaskRef{Key: key}) })
		created, err := d.Ensure(ctx, h.Spec(key, "first", 0, false))
		if err != nil {
			t.Fatal(err)
		}
		if got, err := d.Ensure(ctx, h.Spec(key, "second", 0, false)); !errors.Is(err, execution.ErrTaskConflict) {
			t.Fatalf("a different request under the key: %+v %v", got, err)
		}
		if still, err := d.Observe(ctx, execution.TaskRef{Key: key}); err != nil || still.ID != created.ID || still.State != execution.TaskCreated {
			t.Fatalf("the refusal touched the existing task: %+v %v", still, err)
		}
	})

	t.Run("stopping a task ends it", func(t *testing.T) {
		key := h.Key("stop")
		t.Cleanup(func() { _ = d.Release(context.Background(), execution.TaskRef{Key: key}) })
		created, err := d.Ensure(ctx, h.Spec(key, "", 0, true))
		if err != nil {
			t.Fatal(err)
		}
		ref := execution.TaskRef{Key: key, ProviderID: created.ID}
		if err := d.Start(ctx, ref); err != nil {
			t.Fatal(err)
		}
		var running execution.TaskObservation
		if !poll.Until(ctx, 30*time.Second, 50*time.Millisecond, func() bool {
			running, err = d.Observe(ctx, ref)
			return err == nil && running.State == execution.TaskRunning
		}) {
			t.Fatalf("never running: %+v %v", running, err)
		}
		if err := d.Stop(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if stopped, err := d.Observe(ctx, ref); err != nil || stopped.State != execution.TaskExited {
			t.Fatalf("after stop: %+v %v", stopped, err)
		}
		if err := d.Stop(ctx, ref); err != nil {
			t.Fatalf("stopping an exited task: %v", err)
		}
	})

	t.Run("requests it cannot honour are refused before anything is created", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			change func(*execution.TaskSpec)
			want   error
		}{
			{"expired", func(s *execution.TaskSpec) { s.Admission.Expires = time.Now().Add(-time.Minute) }, execution.ErrExpired},
			{"unknown network policy", func(s *execution.TaskSpec) { s.Admission.Network = "customer-vpn" }, execution.ErrUnsupported},
			{"no profile", func(s *execution.TaskSpec) { s.Admission.Profile = "" }, nil},
		} {
			key := h.Key("refused-" + strings.ReplaceAll(tc.name, " ", "-"))
			spec := h.Spec(key, "", 0, false)
			tc.change(&spec)
			_, err := d.Ensure(ctx, spec)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("%s: %v, want a refusal %v", tc.name, err, tc.want)
			}
			if absent, obsErr := d.Observe(ctx, execution.TaskRef{Key: key}); obsErr != nil || absent.State != execution.TaskAbsent {
				t.Fatalf("%s: a refused request created %+v %v", tc.name, absent, obsErr)
			}
		}
	})
}

// UniqueKeys returns a Key function whose keys are unique to one run.
func UniqueKeys(prefix string) func(string) string {
	stamp := time.Now().UnixNano()
	return func(name string) string { return fmt.Sprintf("%s-%d-%s", prefix, stamp, name) }
}
