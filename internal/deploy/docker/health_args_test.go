package docker

import (
	"slices"
	"testing"
)

// A need's health check runs every second while it starts and every half
// minute after: each check starts a process in the container, so a short
// interval for life costs a small host a large share of its CPU.
func TestHealthChecksAreFrequentOnlyWhileAServiceStarts(t *testing.T) {
	args := healthArgs("pg_isready")
	flag := func(name string) string {
		i := slices.Index(args, name)
		if i < 0 || i+1 >= len(args) {
			t.Fatalf("%s missing from %q", name, args)
		}
		return args[i+1]
	}
	for name, want := range map[string]string{
		"--health-start-interval": "1s",
		"--health-start-period":   healthBound.String(),
		"--health-interval":       "30s",
		"--health-retries":        "3",
	} {
		if got := flag(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if healthArgs("") != nil {
		t.Error("a need without a health check gets no health flags")
	}
}
