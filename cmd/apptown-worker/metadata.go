package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/containerresult"
	dockercli "github.com/flokkit/apptown/internal/deploy/docker"
)

// metadataAddress is the cloud providers' instance metadata service.
const metadataAddress = "169.254.169.254"

// metadataProbe asks for the address's root from a container and prints
// wget's own words and then its exit status: what the address said, if
// anything, is read from them, not from success alone.
var metadataProbe = fmt.Sprintf("wget -T 2 -O /dev/null http://%s/ 2>&1; echo APPTOWN-EXIT=$?", metadataAddress)

// metadataReach says whether a container on this host's Docker reaches the
// cloud metadata service: "reachable", "not reachable", or "could not tell"
// with why. Any HTTP answer, an error status included (a provider asking
// for a token answers 401), is reachable. A probe that did not run, or
// whose answer did not come back (it is read from a file in the container,
// containerresult.Run), is never "not reachable": the install blocks the
// address (design/operations/workers.md), and this is the operator's check
// that it did, not a witness the worker relies on.
func metadataReach(ctx context.Context, docker dockercli.Client, image string) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := (dockercli.Scope{Client: docker}).Result(ctx, containerresult.Spec{Image: image, Script: metadataProbe})
	if err != nil {
		return fmt.Sprintf("could not tell: the probe gave no answer (%v)", err)
	}
	return readMetadataProbe(string(out))
}

// readMetadataProbe reads the probe's output (busybox wget's words).
func readMetadataProbe(out string) string {
	words := strings.ToLower(out)
	switch {
	case !strings.Contains(out, "APPTOWN-EXIT="):
		return "could not tell: the probe gave no answer: " + strings.TrimSpace(out)
	case strings.Contains(out, "APPTOWN-EXIT=0"), strings.Contains(words, "server returned error"):
		return "reachable"
	case strings.Contains(words, "timed out"), strings.Contains(words, "no route"), strings.Contains(words, "network is unreachable"), strings.Contains(words, "host is unreachable"):
		return "not reachable"
	}
	return "could not tell: " + strings.TrimSpace(out)
}
