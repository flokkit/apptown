package main

import (
	"strings"
	"testing"
)

// forge-worker -version names its build and the forge protocols it serves.
func TestTheWorkerVersionNamesItsBuildAndProtocols(t *testing.T) {
	t.Parallel()
	if got := versionLine(); !strings.HasPrefix(got, "forge-worker dev-") || !strings.Contains(got, "serves forge protocols 1 to 2") {
		t.Fatalf("version line: %q", got)
	}
}
