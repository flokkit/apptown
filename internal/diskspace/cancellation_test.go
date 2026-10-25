package diskspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A disk admission already cancelled before it starts owns no container.
// Its cleanup must not escape cancellation and call the daemon anyway.
func TestACancelledDiskReadOwnsNoContainer(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var reached []string
	dk := func(ctx context.Context, args ...string) (string, string, error) {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		reached = append(reached, strings.Join(args, " "))
		return "", "", nil
	}
	_, err := ReadThroughDocker(ctx, dk, "busybox:1.37", "/var/lib/docker", time.Now)
	if len(reached) != 0 {
		t.Fatalf("cancelled disk read called Docker without owning a container: %v", reached)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled disk read returned %v", err)
	}
}
