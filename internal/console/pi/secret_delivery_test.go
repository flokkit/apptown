package pi

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/console"
)

type secretEnvironment struct{ p console.Provision }

func (e *secretEnvironment) Prepare(context.Context, *console.Session) (console.Provision, error) {
	return e.p, nil
}

// Exercise the real console wake and pi runtime together. Docker's create
// environment is immutable, so every changed grant must create a new container.
func TestChatWakeRecreatesTheContainerWhenGivenSecretsChange(t *testing.T) {
	r, _, f := fixture(t)
	r.cfg.Binary = "/usr/bin/true"
	labels := map[string]string{}
	var creates [][]string
	r.cfg.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" && strings.HasPrefix(args[len(args)-1], "apptown-console-") && !strings.HasPrefix(args[len(args)-1], "apptown-console-script-") {
			f.id = strings.TrimPrefix(args[len(args)-1], "apptown-console-")
			if f.console {
				raw, _ := json.Marshal(labels)
				return append([]byte("false 2026-09-09T00:00:00Z img "), raw...), nil
			}
		}
		if args[0] == "image" {
			return []byte("img"), nil
		}
		if args[0] == "create" {
			labels = map[string]string{}
			for i, a := range args {
				if a == "--label" {
					k, v, _ := strings.Cut(args[i+1], "=")
					labels[k] = v
				}
			}
			creates = append(creates, slices.Clone(args))
		}
		return f.run(ctx, args...)
	}
	var recorded []string
	var pending int
	e := &secretEnvironment{p: console.Provision{SecretDigest: "no-secrets"}}
	e.p.Creating = func(context.Context) error { pending++; return nil }
	e.p.Created = func(_ context.Context, container string) error { recorded = append(recorded, container); return nil }
	chats := console.New(console.Dependencies{Agent: r, Environment: e, Config: console.Config{DataDir: t.TempDir()}})
	sess, err := chats.Open(t.Context(), console.OpenRequest{Scope: "change:1", Workspace: console.Workspace{Path: t.TempDir(), Writable: true}, Actor: "boss"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Remove(context.Background(), sess.ID) })
	for _, step := range []struct {
		digest, value string
		count         int
	}{
		{"version-1", "PASSWORD=first-value", 2},
		{"version-1", "PASSWORD=first-value", 2}, // waking an unchanged container gives nothing new
		{"version-2", "PASSWORD=second-value", 3},
		{"no-secrets", "", 4},
	} {
		e.p.SecretDigest = step.digest
		e.p.Env = nil
		if step.value != "" {
			e.p.Env = []string{step.value}
		}
		if err := chats.Warm(t.Context(), sess.ID); err != nil {
			t.Fatal(err)
		}
		if len(creates) != step.count || len(recorded) != step.count || pending != step.count {
			t.Fatalf("creates=%d deliveries=%d want=%d", len(creates), len(recorded), step.count)
		}
		actual := creates[len(creates)-1]
		if step.value != "" && !slices.Contains(actual, step.value) {
			t.Fatal("container did not receive current value")
		}
		if step.value == "" && (slices.Contains(actual, "PASSWORD=first-value") || slices.Contains(actual, "PASSWORD=second-value")) {
			t.Fatal("disabled value still in container")
		}
	}
	if recorded[0] == recorded[1] || recorded[1] == recorded[2] {
		t.Fatal("deliveries did not name distinct containers")
	}
}

// The durable expected-delivery record must precede every container effect.
func TestFailingDeliveryRecordPreventsContainerStart(t *testing.T) {
	r, _, f := fixture(t)
	r.cfg.Binary = "/usr/bin/true"
	recordErr := errors.New("delivery record unavailable")
	e := &secretEnvironment{p: console.Provision{SecretDigest: "version-1", Env: []string{"PASSWORD=value"}}}
	e.p.Creating = func(context.Context) error { return recordErr }
	chats := console.New(console.Dependencies{Agent: r, Environment: e, Config: console.Config{DataDir: t.TempDir()}})
	_, err := chats.Open(t.Context(), console.OpenRequest{Scope: "change:1", Workspace: console.Workspace{Path: t.TempDir(), Writable: true}, Actor: "boss"})
	if !errors.Is(err, recordErr) {
		t.Fatalf("open error = %v", err)
	}
	for _, call := range f.calls {
		if call[0] == "create" || call[0] == "start" {
			t.Fatalf("container effect before record: %s", call[0])
		}
	}
}

func TestFailedContainerCreationSettlesDeliveryAsNotDelivered(t *testing.T) {
	r, _, f := fixture(t)
	f.fail = "create"
	pending := false
	settled := false
	e := &secretEnvironment{p: console.Provision{SecretDigest: "version-1"}}
	e.p.Creating = func(context.Context) error { pending = true; return nil }
	e.p.Created = func(_ context.Context, container string) error {
		if !pending || container != "" {
			t.Fatalf("pending=%v container=%q", pending, container)
		}
		settled = true
		return nil
	}
	chats := console.New(console.Dependencies{Agent: r, Environment: e, Config: console.Config{DataDir: t.TempDir()}})
	if _, err := chats.Open(t.Context(), console.OpenRequest{Scope: "change:1", Workspace: console.Workspace{Path: t.TempDir()}, Actor: "boss"}); err == nil {
		t.Fatal("failed creation succeeded")
	}
	if !settled {
		t.Fatal("failed creation did not settle delivery")
	}
}
