package containerresult

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

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
// either makes a new one, and the class's others are removed once no read
// in this process is using them. It is started again if it stopped, made
// again only when it is gone, and kept across daemon restarts.
//
// A process finds, makes, starts and removes a class's readers one call at
// a time: docker run reserves a name before the container is there to
// exec in, so a second caller that took "already in use" as made could
// exec into nothing. A reader gone when the script was to run (removed by
// a person, a restart or another process on the host) is made again once
// and read again; a read that fails then is an error, never a read
// through a throwaway container instead.
func RunStanding(ctx context.Context, dk Docker, class string, s Spec) ([]byte, error) {
	if class == "" || s.Image == "" {
		return nil, fmt.Errorf("a standing reader needs a class and an image")
	}
	name := standingName(class, s)
	c := standingClassOf(class)
	out, err := c.read(ctx, dk, class, name, s)
	if errors.Is(err, ErrNoContainer) {
		out, err = c.read(ctx, dk, class, name, s)
	}
	return out, err
}

// standingClass is this process's hold on one class of standing readers.
type standingClass struct {
	mu sync.Mutex
	// changing is a call finding, making, starting or removing the class's
	// readers; the others wait on changed. A condition, not a lock held
	// across the docker calls, so mu stays free for inUse meanwhile.
	changing bool
	changed  sync.Cond
	// using counts the reads in each reader now, by name. It only grows
	// while changing, so a reader seen unused then stays unused.
	using map[string]int
}

var standingClasses sync.Map // class -> *standingClass

func standingClassOf(class string) *standingClass {
	if c, ok := standingClasses.Load(class); ok {
		return c.(*standingClass)
	}
	c := &standingClass{using: map[string]int{}}
	c.changed.L = &c.mu
	got, _ := standingClasses.LoadOrStore(class, c)
	return got.(*standingClass)
}

// read leaves the reader running and runs s.Script in it, counted as
// using it for as long as the script runs.
func (c *standingClass) read(ctx context.Context, dk Docker, class, name string, s Spec) ([]byte, error) {
	c.mu.Lock()
	for c.changing {
		c.changed.Wait()
	}
	c.changing = true
	c.mu.Unlock()
	err := ensureStanding(ctx, dk, class, name, s, c.inUse)
	c.mu.Lock()
	if err == nil {
		c.using[name]++
	}
	c.changing = false
	c.mu.Unlock()
	c.changed.Signal()
	if err != nil {
		return nil, err
	}
	defer func() {
		c.mu.Lock()
		if c.using[name]--; c.using[name] == 0 {
			delete(c.using, name)
		}
		c.mu.Unlock()
	}()
	return RunIn(ctx, dk, name, Spec{Script: s.Script, Env: s.Env, Max: s.Max, Validate: s.Validate})
}

func (c *standingClass) inUse(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.using[name] > 0
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
// The class's other readers (an older image's or options') are removed
// unless inUse says a read is using them.
func ensureStanding(ctx context.Context, dk Docker, class, name string, s Spec, inUse func(string) bool) error {
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
	for _, other := range others {
		if inUse(other) {
			continue
		}
		if out, err := dk(ctx, nil, "rm", "-f", other); err != nil {
			return step("removing the replaced reader", other, err, out)
		}
	}
	switch state {
	case "running":
		return nil
	case "":
		run := []string{"run", "-d", "--name", name, "--label", dockerlabel.Class + "=" + class,
			"--restart", "unless-stopped", "--entrypoint", ""}
		run = append(run, s.Options...)
		run = append(run, s.Image, "sleep", "2147483647")
		if out, err := dk(ctx, nil, run...); err != nil {
			if strings.Contains(string(out), "is already in use") {
				return nil // another process on the host is making it
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
