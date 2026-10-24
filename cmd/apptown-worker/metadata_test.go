package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	dockercli "github.com/flokkit/apptown/internal/deploy/docker"
)

// probeAnswer is a daemon whose probe container leaves out in its answer
// file (nil: none) and keeps its stdout to itself; err refuses the
// container's creation.
type probeAnswer struct {
	out []byte
	err error
	d   *containerresulttest.Daemon
}

func (p *probeAnswer) Run(_ context.Context, _ string, c dockercli.Command) ([]byte, error) {
	if p.err != nil && c.Args[0] == "create" {
		return []byte("Unable to find image"), p.err
	}
	if p.d == nil {
		p.d = &containerresulttest.Daemon{Answers: func(containerresulttest.Command) (containerresulttest.Answer, bool) {
			return containerresulttest.Answer{Answer: p.out}, true
		}}
	}
	out, err, _ := p.d.Run(c.Args)
	return out, err
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
		answer *probeAnswer
		want   string
	}{
		{&probeAnswer{out: []byte("APPTOWN-EXIT=0\n")}, "reachable"},
		{&probeAnswer{out: []byte("Connecting to 169.254.169.254 (169.254.169.254:80)\nwget: server returned error: HTTP/1.1 401 Unauthorized\nAPPTOWN-EXIT=1\n")}, "reachable"},
		{&probeAnswer{out: []byte("wget: server returned error: HTTP/1.1 404 Not Found\nAPPTOWN-EXIT=1\n")}, "reachable"},
		{&probeAnswer{out: []byte("Connecting to 169.254.169.254 (169.254.169.254:80)\nwget: download timed out\nAPPTOWN-EXIT=1\n")}, "not reachable"},
		{&probeAnswer{out: []byte("wget: can't connect to remote host (169.254.169.254): Network is unreachable\nAPPTOWN-EXIT=1\n")}, "not reachable"},
		{&probeAnswer{out: []byte("wget: can't connect to remote host (169.254.169.254): Connection refused\nAPPTOWN-EXIT=1\n")}, "could not tell"},
		{&probeAnswer{out: []byte("Unable to find image"), err: errors.New("exit 125")}, "could not tell"},
		{&probeAnswer{out: []byte("")}, "could not tell"},
		{&probeAnswer{}, "could not tell"}, // the answer did not come back
	} {
		if got := metadataReach(context.Background(), tc.answer, "docker:27-dind"); !strings.HasPrefix(got, tc.want) {
			t.Errorf("%q, %v: %q, want %q", tc.answer.out, tc.answer.err, got, tc.want)
		}
	}
}
