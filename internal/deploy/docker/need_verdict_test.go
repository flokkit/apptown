package docker

import (
	"context"
	"testing"

	"flokkit.io/forge/internal/deploy"
)

// Docker's own verdict on a need ends a deployment attempt as a known
// failure: nothing of the app was started. A wait the caller ended stays
// unknown.
func TestANeedDockerCallsUnhealthyIsAKnownFailure(t *testing.T) {
	t.Parallel()
	svcs := []deploy.ServiceSpec{{Name: "postgres", Health: "pg_isready"}}
	daemon := func(status string) Scope {
		return Scope{Client: &fakeClient{reply: func(Command) ([]byte, error) { return []byte(status), nil }}}
	}
	if _, err := waitStackHealthy(context.Background(), daemon("unhealthy"), svcs, "s"); err == nil || !deploy.IsTerminal(err) {
		t.Fatalf("an unhealthy need is not a known failure: %v", err)
	}
	if _, err := waitStackHealthy(context.Background(), daemon("healthy"), svcs, "s"); err != nil {
		t.Fatalf("a healthy need failed the wait: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitStackHealthy(ctx, daemon("starting"), svcs, "s"); err == nil || deploy.IsTerminal(err) {
		t.Fatalf("a wait ended by its caller claims a known outcome: %v", err)
	}
}
