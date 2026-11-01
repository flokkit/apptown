package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

func TestExplicitLivenessUsesCadenceAndSameRuntimeFailures(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprint(replace), func(t *testing.T) {
			now := time.Unix(1000, 0)
			identity := deploy.DockerIdentity{Deployment: "selected", Generation: 7, Artifact: "artifact"}
			rt := Runtime{ID: "captured-a", Running: true, Labels: identity.Labels(), State: &ContainerState{StartedAt: time.Unix(1, 0)}}
			probes, restarts := 0, 0
			dk := Scope{Client: &fakeClient{reply: func(cmd Command) ([]byte, error) {
				switch cmd.Args[0] {
				case "inspect":
					return json.Marshal(rt)
				case "exec":
					probes++
					return nil, fmt.Errorf("unhealthy")
				case "restart":
					restarts++
					if cmd.Args[1] != rt.ID {
						t.Errorf("restart used mutable name: %v", cmd.Args)
					}
					return nil, nil
				default:
					return nil, fmt.Errorf("unexpected %v", cmd.Args)
				}
			}}}
			part := &appcontract.Part{Public: true, Port: 8080, Intent: &appcontract.Health{Liveness: &appcontract.Probe{Exec: []any{"false"}, Period: "5s", Timeout: "1s", FailureThreshold: 2, SuccessThreshold: 2}}}
			c := NewHealthController(HealthDependencies{Now: func() time.Time { return now }, FrontDoor: func() map[string]string { return nil }, Record: func(context.Context, Watched, string, map[string]any) {}})
			ph := &partHealth{}
			e := Watched{Container: "mutable-name", Identity: identity}
			for i := 0; i < 5; i++ {
				c.explicitLiveness(t.Context(), e, dk, ph, part)
				now = now.Add(time.Second)
			}
			if probes != 1 || restarts != 0 {
				t.Fatalf("cadence/threshold probes=%d restarts=%d", probes, restarts)
			}
			if replace {
				rt.ID = "captured-b"
			}
			c.explicitLiveness(t.Context(), e, dk, ph, part)
			want := 1
			if replace {
				want = 0
			}
			if restarts != want {
				t.Fatalf("replacement=%v restarts=%d want%d", replace, restarts, want)
			}
			if replace {
				now = now.Add(5 * time.Second)
				c.explicitLiveness(t.Context(), e, dk, ph, part)
				if restarts != 1 {
					t.Fatal("same replacement never reached its own threshold")
				}
			}
			if !strings.HasPrefix(ph.runtimeToken, rt.ID+"/") {
				t.Fatal("runtime token not retained")
			}
		})
	}
}

type livenessContextClient func(context.Context, string, Command) ([]byte, error)

func (f livenessContextClient) Run(ctx context.Context, host string, cmd Command) ([]byte, error) {
	return f(ctx, host, cmd)
}
func TestLivenessOmittedExecKeepsCallerDeadline(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		seen := false
		dk := Scope{Client: livenessContextClient(func(ctx context.Context, _ string, _ Command) ([]byte, error) {
			seen = true
			_, has := ctx.Deadline()
			if has != explicit {
				t.Errorf("explicit=%v has deadline=%v", explicit, has)
			}
			return nil, nil
		})}
		rp := renderedProbe{Exec: []string{"check"}}
		if explicit {
			rp.Policy = &appcontract.HealthPolicy{Explicit: true, Timeout: time.Second}
		}
		if !probeLiveness(context.Background(), dk, "captured", 0, rp, nil) || !seen {
			t.Fatal("exec not sampled")
		}
	}
}

func TestUnreadableRuntimeDoesNotForgetExplicitLiveness(t *testing.T) {
	calls := 0
	dk := Scope{Client: livenessContextClient(func(context.Context, string, Command) ([]byte, error) { calls++; return nil, fmt.Errorf("unavailable") })}
	c := NewHealthController(HealthDependencies{Now: func() time.Time { return time.Unix(1000, 0) }})
	ph := &partHealth{}
	part := &appcontract.Part{Intent: &appcontract.Health{Liveness: &appcontract.Probe{HTTP: "/alive", Period: "5s"}}}
	if !c.explicitLiveness(t.Context(), Watched{}, dk, ph, part) {
		t.Fatal("explicit not handled")
	}
	for range 5 {
		if !c.explicitLiveness(t.Context(), Watched{}, dk, ph, nil) {
			t.Fatal("unreadable source fell back to default")
		}
	}
	if calls != 1 {
		t.Fatalf("unexpected fallback effects: %d", calls)
	}
}

func TestExplicitLivenessOwnRestartsRetainCappedBackoff(t *testing.T) {
	now := time.Unix(1000, 0)
	identity := deploy.DockerIdentity{Deployment: "owned", Generation: 1}
	rt := Runtime{ID: "same-container", Running: true, Labels: identity.Labels(), State: &ContainerState{StartedAt: time.Unix(1, 0)}}
	var restarted []time.Time
	dk := Scope{Client: &fakeClient{reply: func(cmd Command) ([]byte, error) {
		switch cmd.Args[0] {
		case "inspect":
			return json.Marshal(rt)
		case "exec":
			return nil, fmt.Errorf("hung")
		case "restart":
			restarted = append(restarted, now)
			rt.State.StartedAt = now
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected command")
		}
	}}}
	c := NewHealthController(HealthDependencies{Now: func() time.Time { return now }, FrontDoor: func() map[string]string { return nil }, Record: func(context.Context, Watched, string, map[string]any) {}})
	ph := &partHealth{}
	e := Watched{Container: "mutable", Identity: identity}
	part := &appcontract.Part{Intent: &appcontract.Health{Liveness: &appcontract.Probe{Exec: []any{"false"}, Period: "5s", FailureThreshold: 1}}}
	for i := 0; i < 250; i++ {
		c.explicitLiveness(t.Context(), e, dk, ph, part)
		now = now.Add(5 * time.Second)
	}
	if len(restarted) < 7 {
		t.Fatalf("not enough restarts to prove cap: %v", restarted)
	}
	for i, want := range []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second, 240 * time.Second, 300 * time.Second, 300 * time.Second} {
		if got := restarted[i+1].Sub(restarted[i]); got != want {
			t.Fatalf("restart%d interval=%s want%s", i+1, got, want)
		}
	}
}
