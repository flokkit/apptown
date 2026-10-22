package apptown

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/poll"
)

func TestLoopbackAddr(t *testing.T) {
	t.Parallel()
	for addr, want := range map[string]bool{
		"127.0.0.1:7777": true,
		"localhost:7777": true,
		":7777":          false,
		"0.0.0.0:7777":   false,
		"192.168.1.5:80": false,
	} {
		if got := loopbackAddr(addr); got != want {
			t.Errorf("loopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

// Open authentication serves only this machine unless the operator says
// otherwise: in open mode a caller names itself, so a listener another
// machine can reach is full control for anyone who reaches it. The other
// modes, and loopback listeners, start as they always have.
//
// Kills: a warning on stderr as the only guard, and a check that looked at
// the main listener but not the console's.
func TestOpenModeServesOnlyThisMachineUnlessAsked(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mode    string
		addr    string
		console string
		allowed bool
		refused []string // listeners the refusal must name
	}{
		{"the old default", "open", ":7777", ":7778", false, []string{`-addr ":7777"`, `-console-addr ":7778"`}},
		{"a loopback main beside a reachable console", "open", "127.0.0.1:7777", ":7778", false, []string{`-console-addr ":7778"`}},
		{"an address on the network", "open", "192.168.1.5:7777", "", false, []string{`-addr "192.168.1.5:7777"`}},
		{"an empty auth mode is open mode", "", ":7777", "", false, []string{`-addr ":7777"`}},
		{"an empty main address is every interface", "open", "", "", false, []string{`-addr ""`}},
		{"loopback, both", "open", "127.0.0.1:7777", "127.0.0.1:7778", false, nil},
		{"IPv6 loopback and localhost", "open", "[::1]:7777", "localhost:7778", false, nil},
		{"no console listener", "open", "127.0.0.1:7777", "", false, nil},
		{"the operator accepts it", "open", ":7777", ":7778", true, nil},
		{"internal mode on every interface", "internal", ":7777", ":7778", false, nil},
		{"trusted-header mode", "trusted-header", ":7777", ":7778", false, nil},
	}
	for _, c := range cases {
		err := OpenExposure(c.mode, c.addr, c.console, c.allowed)
		if len(c.refused) == 0 {
			if err != nil {
				t.Errorf("%s: refused: %v", c.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: started", c.name)
			continue
		}
		for _, want := range append(c.refused, "-open-on-network", "127.0.0.1") {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal does not name %q: %v", c.name, want, err)
			}
		}
	}
}

// recordingDocker records each Docker command and fails them all when told to.
type recordingDocker struct {
	mu       sync.Mutex
	commands []string
	fail     bool
}

func (r *recordingDocker) Run(_ context.Context, _ string, req docker.Command) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, strings.Join(req.Args, " "))
	if r.fail {
		return []byte("denied"), errors.New("exit status 1")
	}
	return nil, nil
}

func (r *recordingDocker) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.commands...)
}

// agentImageSettled waits for the background fetch to end and returns the state.
func agentImageSettled(t *testing.T, s *setupState) (string, string) {
	t.Helper()
	var state, reason string
	if !poll.Until(t.Context(), 10*time.Second, 10*time.Millisecond, func() bool {
		state, reason = s.ImageState()
		return state != "pulling" && state != "building"
	}) {
		t.Fatalf("the agent image is still %q", state)
	}
	return state, reason
}

// An agent image named in a registry (the desktop trial's
// ghcr.io/flokkit/apptown-agent:<version>) is pulled when missing, never
// built under the published name, and so needs no build context beside the
// binary. A failed pull says so.
func TestAMissingAgentImageInARegistryIsPulledNotBuilt(t *testing.T) {
	t.Parallel()
	const image = "ghcr.io/flokkit/apptown-agent:1.0.0"
	for _, fail := range []bool{false, true} {
		var s setupState
		rec := &recordingDocker{fail: fail}
		dir := t.TempDir() // no Dockerfile there
		s.fetch(t.Context(), docker.Scope{Client: rec}, image, dir, filepath.Join(dir, "log"), func(string) {})
		state, reason := agentImageSettled(t, &s)
		if got := rec.seen(); len(got) != 1 || got[0] != "pull "+image {
			t.Fatalf("commands: %v", got)
		}
		switch {
		case !fail && state != "present":
			t.Fatalf("after a pull: %q %q", state, reason)
		case fail && (state != "failed" || !strings.Contains(reason, "pull of "+image+" failed")):
			t.Fatalf("after a failed pull: %q %q", state, reason)
		}
	}
	for _, ref := range []string{"apptown-agent", "apptown-agent:abc123", "library/ubuntu"} {
		if namesRegistry(ref) {
			t.Fatalf("%s names no registry", ref)
		}
	}
	if !namesRegistry("localhost:5000/agent") {
		t.Fatal("localhost:5000/agent names a registry")
	}
}

// A bare agent image name is still built from the build context.
func TestAMissingBareAgentImageIsBuilt(t *testing.T) {
	t.Parallel()
	var s setupState
	rec := &recordingDocker{}
	s.fetch(t.Context(), docker.Scope{Client: rec}, "apptown-agent", "../../deploy/apptown-agent", filepath.Join(t.TempDir(), "log"), func(string) {})
	if state, reason := agentImageSettled(t, &s); state != "present" {
		t.Fatalf("after a build: %q %q", state, reason)
	}
	if got := rec.seen(); len(got) != 1 || got[0] != "build -t apptown-agent ../../deploy/apptown-agent" {
		t.Fatalf("commands: %v", got)
	}
}

// labelledImageDocker is a daemon holding the agent image, with or without
// AppTown's version label, that records every command.
type labelledImageDocker struct {
	recordingDocker
	version string // "" is an image without apptown.pi.version
}

func (d *labelledImageDocker) Run(ctx context.Context, binary string, req docker.Command) ([]byte, error) {
	_, _ = d.recordingDocker.Run(ctx, binary, req)
	args := strings.Join(req.Args, " ")
	switch {
	case strings.HasPrefix(args, "info"):
		return []byte("ok"), nil
	case strings.HasPrefix(args, "image inspect -f") && strings.Contains(args, dockerlabel.PiVersion):
		return []byte(d.version), nil
	}
	return nil, nil
}

// The agent image is fetched again when it lacks AppTown's version label
// (one built before 1.0 carries forge.pi.version, so every step would
// record no versions); one that carries it is left as it is.
//
// Kills: an image that exists taken as present whatever it carries.
func TestAnAgentImageWithoutAppTownsVersionLabelIsFetchedAgain(t *testing.T) {
	t.Parallel()
	const image = "ghcr.io/flokkit/apptown-agent:1.0.0"
	for version, fetched := range map[string]bool{"": true, "0.71.0": false} {
		d := &labelledImageDocker{version: version}
		a := testApp(t, Config{DockerClient: d, AgentImage: image})
		a.EnsureAgentImage()
		state, _ := agentImageSettled(t, &a.setup)
		pulled := slices.Contains(d.seen(), "pull "+image)
		if pulled != fetched || state != "present" {
			t.Errorf("an image with version label %q: pulled %v, state %q (%v)", version, pulled, state, d.seen())
		}
	}
}
