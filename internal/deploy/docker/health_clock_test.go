package docker

import (
	"context"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy/appcontract"
)

// A hung part's restart backoff is judged on the controller's clock: once
// it says the backoff has passed, the part is restarted again, however
// little real time passed.
func TestAHungPartsBackoffIsTheControllersClocks(t *testing.T) {
	t.Parallel()
	// A port nothing listens on: the liveness probe (a TCP connect) fails.
	// Port 1 (privileged) rather than a freed ephemeral port, which another
	// test running in parallel could bind and answer from (it did under the
	// full gate's load, 11 Oct).
	port := 1
	restarts := 0
	client := &fakeClient{reply: func(c Command) ([]byte, error) {
		switch {
		case c.Args[0] == "restart":
			restarts++
			return nil, nil
		case strings.Contains(strings.Join(c.Args, " "), "RestartCount"):
			return []byte("0"), nil
		case strings.Contains(strings.Join(c.Args, " "), "State.Running"):
			return []byte("true"), nil
		case strings.Contains(strings.Join(c.Args, " "), "StartedAt"):
			return []byte("2001-01-01T00:00:00Z"), nil // long past its startup grace
		}
		return nil, nil
	}}
	at := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	hc := NewHealthController(HealthDependencies{
		Watch: func(context.Context) []Watched {
			return []Watched{{Slug: "shop", Name: "main", HostPort: port, Container: "apptown-env-shop-main", Docker: Scope{Client: client}}}
		},
		PublicPart: func(context.Context, Watched) *appcontract.Part { return nil },
		FrontDoor:  func() map[string]string { return nil },
		Record:     func(context.Context, Watched, string, map[string]any) {},
		Now:        func() time.Time { return at },
	})
	sweeps := func() {
		for range HealthFailThreshold {
			hc.Sweep(context.Background())
		}
	}
	sweeps()
	if restarts != 1 {
		t.Fatalf("a hung part was restarted %d times, want once", restarts)
	}
	sweeps()
	if restarts != 1 {
		t.Fatalf("restarted again inside its backoff: %d", restarts)
	}
	at = at.Add(HealthBackoffCap) // the backoff has passed on the controller's clock
	sweeps()
	if restarts != 2 {
		t.Fatalf("not restarted after its backoff on the controller's clock: %d", restarts)
	}
}
