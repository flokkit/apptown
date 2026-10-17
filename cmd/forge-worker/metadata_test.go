package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	dockercli "flokkit.io/forge/internal/deploy/docker"
)

type probeAnswer struct {
	out []byte
	err error
}

func (p probeAnswer) Run(context.Context, string, dockercli.Command) ([]byte, error) {
	return p.out, p.err
}

// The diagnostic reads what the address said: any HTTP answer, an error
// status included, is reachable; a timeout or no route is not; a refusal,
// or a probe that did not run, is "could not tell", never blocked. The
// words are busybox wget's, as the probe image prints them.
//
// Kills: a probe that read every unsuccessful wget, an HTTP 401 among
// them, as not reachable (review CT-C02).
func TestTheMetadataCheckReadsWhatTheAddressSaid(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		answer probeAnswer
		want   string
	}{
		{probeAnswer{out: []byte("FORGE-EXIT=0\n")}, "reachable"},
		{probeAnswer{out: []byte("Connecting to 169.254.169.254 (169.254.169.254:80)\nwget: server returned error: HTTP/1.1 401 Unauthorized\nFORGE-EXIT=1\n")}, "reachable"},
		{probeAnswer{out: []byte("wget: server returned error: HTTP/1.1 404 Not Found\nFORGE-EXIT=1\n")}, "reachable"},
		{probeAnswer{out: []byte("Connecting to 169.254.169.254 (169.254.169.254:80)\nwget: download timed out\nFORGE-EXIT=1\n")}, "not reachable"},
		{probeAnswer{out: []byte("wget: can't connect to remote host (169.254.169.254): Network is unreachable\nFORGE-EXIT=1\n")}, "not reachable"},
		{probeAnswer{out: []byte("wget: can't connect to remote host (169.254.169.254): Connection refused\nFORGE-EXIT=1\n")}, "could not tell"},
		{probeAnswer{out: []byte("Unable to find image"), err: errors.New("exit 125")}, "could not tell"},
		{probeAnswer{out: []byte("")}, "could not tell"},
	} {
		if got := metadataReach(context.Background(), tc.answer, "docker:27-dind"); !strings.HasPrefix(got, tc.want) {
			t.Errorf("%q, %v: %q, want %q", tc.answer.out, tc.answer.err, got, tc.want)
		}
	}
}
