package diskspace

import (
	"context"
	"slices"
	"testing"
	"time"
)

// A disk read through Docker runs df in the image its caller gives it (the
// tool versions' df, from the caller's snapshot), and without one reads
// nothing.
func TestADiskReadRunsInTheImageItIsGiven(t *testing.T) {
	t.Parallel()
	var created []string
	dk := func(_ context.Context, args ...string) (string, string, error) {
		if len(args) > 0 && args[0] == "create" {
			created = args
		}
		return "", "", context.Canceled
	}
	_, _ = ReadThroughDocker(t.Context(), dk, "example.invalid/df@sha256:0", "/var/lib/docker", time.Now)
	if !slices.Contains(created, "example.invalid/df@sha256:0") {
		t.Fatalf("the probe was created as %v, not in the given image", created)
	}
	if _, err := ReadThroughDocker(t.Context(), dk, "", "/var/lib/docker", time.Now); err == nil {
		t.Fatal("a read with no image read something")
	}
}
