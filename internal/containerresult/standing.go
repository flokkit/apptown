package containerresult

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/dockerlabel"
)

// StandingPrefix begins the name of a standing reader.
const StandingPrefix = "apptown-reader-"

// RunStanding reads s.Script's answer in the daemon's standing reader of
// class: one long-lived container per daemon and class, made from s.Image
// with s.Options and read with docker exec (RunIn). A read that recurs on a
// timer belongs here, not in Run: every container start costs the host's
// init a scope, a network interface and mounts, and a CT whose systemd kept
// a little of each was killed for memory after weeks of throwaway reads.
// An exec into a running container makes none of them.
//
// The reader is named for its class, image and options, so a change of
// either makes a new one; making one removes the class's others. It is
// started again if it stopped, made again only when it is gone, and kept
// across daemon restarts. A read that fails is an error, never a read
// through a throwaway container instead.
func RunStanding(ctx context.Context, dk Docker, class string, s Spec) ([]byte, error) {
	if class == "" || s.Image == "" {
		return nil, fmt.Errorf("a standing reader needs a class and an image")
	}
	name := standingName(class, s)
	if err := ensureStanding(ctx, dk, class, name, s); err != nil {
		return nil, err
	}
	return RunIn(ctx, dk, name, Spec{Script: s.Script, Env: s.Env, Max: s.Max, Validate: s.Validate})
}

func standingName(class string, s Spec) string {
	h := sha256.New()
	for _, part := range append([]string{s.Image}, s.Options...) {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return StandingPrefix + class + "-" + hex.EncodeToString(h.Sum(nil))[:12]
}

// ensureStanding leaves the reader running: as it is, started, or made.
func ensureStanding(ctx context.Context, dk Docker, class, name string, s Spec) error {
	label := "label=" + dockerlabel.Class + "=" + class
	out, err := dk(ctx, nil, "ps", "-a", "--filter", label, "--format", "{{.Names}}\t{{.State}}")
	if err != nil {
		return step("finding the standing reader", name, err, out)
	}
	state := ""
	var others []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		n, st, _ := strings.Cut(strings.TrimSpace(line), "\t")
		switch {
		case n == "":
		case n == name:
			state = st
		default:
			others = append(others, n)
		}
	}
	switch state {
	case "running":
		return nil
	case "":
		// Gone (or never made): the class's other readers are an older
		// image's or options', replaced by this one.
		for _, other := range others {
			if out, err := dk(ctx, nil, "rm", "-f", other); err != nil {
				return step("removing the replaced reader", other, err, out)
			}
		}
		run := []string{"run", "-d", "--name", name, "--label", dockerlabel.Class + "=" + class,
			"--restart", "unless-stopped", "--entrypoint", ""}
		run = append(run, s.Options...)
		run = append(run, s.Image, "sleep", "2147483647")
		if out, err := dk(ctx, nil, run...); err != nil {
			if strings.Contains(string(out), "is already in use") {
				return nil // another reader of the same daemon made it first
			}
			return step("making the standing reader", name, err, out)
		}
		return nil
	default:
		if out, err := dk(ctx, nil, "start", name); err != nil {
			return step("starting the standing reader", name, err, out)
		}
		return nil
	}
}
