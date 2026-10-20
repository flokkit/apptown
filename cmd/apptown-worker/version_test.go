package main

import (
	"strings"
	"testing"
)

// apptown-worker -version names its build and AppTown protocols it serves.
func TestTheWorkerVersionNamesItsBuildAndProtocols(t *testing.T) {
	t.Parallel()
	if got := versionLine(); !strings.HasPrefix(got, "apptown-worker dev-") || !strings.Contains(got, "serves AppTown protocols 1 to 2") {
		t.Fatalf("version line: %q", got)
	}
}
