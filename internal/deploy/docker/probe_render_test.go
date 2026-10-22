package docker

import (
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// The cold-boot render turns a part's intent into a concrete probe, and — the
// office-poll thrash fix — reproduces the front door's secure signal so a
// hardened app doesn't 301 the naked hit (design/subsystems/healthchecks.md §3, §4.3).
func TestRenderColdBootProbe(t *testing.T) {
	t.Parallel()
	fd := map[string]string{"X-Forwarded-Proto": "https"}

	// No intent → §8 default: GET / shaped by the front door.
	if p := RenderColdBootProbe(nil, fd); p.Path != "/" || p.Headers["X-Forwarded-Proto"] != "https" || p.Expect != 0 {
		t.Fatalf("default probe: %+v", p)
	}

	// A part with no health block is still the fronted default.
	bare := &appcontract.Part{Name: "web", Public: true}
	if p := RenderColdBootProbe(bare, fd); p.Path != "/" || p.Headers == nil {
		t.Fatalf("bare part: %+v", p)
	}

	// http readiness: path + expect carried, shaped by default (frontDoor).
	httpReady := &appcontract.Part{Name: "web", Public: true, Intent: &appcontract.Health{
		Readiness: &appcontract.Probe{HTTP: "/healthz", Expect: 200, StartupGrace: "90s"},
	}}
	p := RenderColdBootProbe(httpReady, fd)
	if p.Path != "/healthz" || p.Expect != 200 || p.Headers["X-Forwarded-Proto"] != "https" || p.Grace != 90*time.Second {
		t.Fatalf("http readiness: %+v", p)
	}

	// requestContext: direct → NOT shaped (the app asserts it needs no signal).
	direct := &appcontract.Part{Name: "web", Public: true, Intent: &appcontract.Health{
		Readiness: &appcontract.Probe{HTTP: "/healthz", RequestContext: "direct"},
	}}
	if p := RenderColdBootProbe(direct, fd); p.Headers != nil {
		t.Fatalf("direct probe should be unshaped: %+v", p)
	}

	// tcp/exec readiness on the public part → HTTP up-probe fallback, still shaped.
	tcp := &appcontract.Part{Name: "web", Public: true, Intent: &appcontract.Health{
		Readiness: &appcontract.Probe{TCP: 8080},
	}}
	if p := RenderColdBootProbe(tcp, fd); p.Path != "/" || p.Headers == nil {
		t.Fatalf("tcp fallback: %+v", p)
	}
}

// renderLivenessProbe: the §8 default is a dependency-free TCP connect; a
// declared http/exec liveness renders to that instead.
func TestRenderLivenessProbe(t *testing.T) {
	t.Parallel()
	if p := RenderLivenessProbe(nil); !p.TCP {
		t.Fatalf("no intent → TCP default, got %+v", p)
	}
	bare := &appcontract.Part{Name: "web", Public: true}
	if p := RenderLivenessProbe(bare); !p.TCP {
		t.Fatalf("no health block → TCP default, got %+v", p)
	}
	httpL := &appcontract.Part{Intent: &appcontract.Health{Liveness: &appcontract.Probe{HTTP: "/livez", Expect: 200}}}
	if p := RenderLivenessProbe(httpL); p.TCP || p.Path != "/livez" || p.Expect != 200 {
		t.Fatalf("http liveness: %+v", p)
	}
	execL := &appcontract.Part{Intent: &appcontract.Health{Liveness: &appcontract.Probe{Exec: []any{"pgrep", "-f", "worker"}}}}
	if p := RenderLivenessProbe(execL); len(p.Exec) != 3 || p.Exec[0] != "pgrep" {
		t.Fatalf("exec liveness: %+v", p)
	}
}
