package docker

import (
	"context"
	"errors"
	"github.com/flokkit/apptown/internal/deploy"
	"testing"
	"time"
)

func TestExpiredSweepDoesNotClaimDockerIsUnreachable(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := Inspect(ctx, Scope{}, "container")
	if !errors.Is(err, context.DeadlineExceeded) || deploy.IsUnreachable(err) {
		t.Fatalf("expired sweep classified as host failure: %v", err)
	}
	_, err = (CLI{Binary: "docker"}).Run(ctx, "", Command{Args: []string{"info"}, Timeout: time.Second})
	if err == nil || deploy.IsUnreachable(err) {
		t.Fatalf("parent deadline classified as command timeout: %v", err)
	}
}
