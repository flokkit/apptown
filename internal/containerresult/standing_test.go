package containerresult_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/flokkit/apptown/internal/containerresult"
	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
)

// creating stands a Docker daemon's docker run of a standing reader in front
// of d: the name is reserved (a second run is refused as already in use)
// before the container is there to exec in, as Docker's create does, and
// a run of a name in hold waits for its channel to close before it is made.
type creating struct {
	d    *containerresulttest.Daemon
	mu   sync.Mutex
	hold map[string]chan struct{}
	made map[string]bool // reserved names, made or being made
	// before, when set, runs before each command reaches the daemon.
	before func(args []string)
}

func (c *creating) dk(_ context.Context, env []string, args ...string) ([]byte, error) {
	if c.before != nil {
		c.before(args)
	}
	if args[0] == "run" && len(args) > 1 && args[1] == "-d" {
		name := flagValue(args, "--name")
		c.mu.Lock()
		if c.made[name] {
			c.mu.Unlock()
			return []byte(`docker: Error response from daemon: Conflict. The container name "/` + name + `" is already in use`), fmt.Errorf("exit status 125")
		}
		c.made[name] = true
		wait := c.hold[name]
		c.mu.Unlock()
		if wait != nil {
			<-wait
		}
	}
	if args[0] == "rm" {
		c.mu.Lock()
		delete(c.made, args[len(args)-1])
		c.mu.Unlock()
	}
	out, err, ok := c.d.RunEnv(args, env)
	if !ok {
		return nil, fmt.Errorf("unexpected command %v", args)
	}
	return out, err
}

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

var classes atomic.Int64

// newClass is a class of reader no other test (or -count run) shares.
func newClass(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, classes.Add(1))
}

func standingDaemon() *containerresulttest.Daemon {
	return &containerresulttest.Daemon{Answers: func(c containerresulttest.Command) (containerresulttest.Answer, bool) {
		return containerresulttest.Answer{Answer: []byte("read " + c.Image)}, c.Script == "read"
	}}
}

func readerName(t *testing.T, d *containerresulttest.Daemon) string {
	t.Helper()
	got := d.Readers()
	if len(got) != 1 {
		t.Fatalf("readers %v, want one", got)
	}
	return got[0]
}

// Two reads of one class in one process while its reader is being made
// both answer from that one reader: the second waits for the first's
// docker run rather than taking "already in use" as made and exec'ing into
// a name Docker has reserved but not yet made (main's heavy run, 30 Oct:
// "No such container: apptown-reader-disk-reader-...").
//
// Kills: a standing reader found and made without a lock per class.
func TestReadsOfOneReaderBeingMadeWaitForIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := standingDaemon()
		release := make(chan struct{})
		c := &creating{d: d, made: map[string]bool{}}
		spec := containerresult.Spec{Image: "busybox@sha256:1", Script: "read"}
		class := newClass("race-made")
		// The reader's name, as a first read makes it: then gone again.
		if _, err := containerresult.RunStanding(t.Context(), c.dk, class, spec); err != nil {
			t.Fatal(err)
		}
		name := readerName(t, d)
		d.StopReader(name, true)
		delete(c.made, name)
		c.hold = map[string]chan struct{}{name: release}

		errs := make(chan error, 2)
		read := func() {
			_, err := containerresult.RunStanding(t.Context(), c.dk, class, spec)
			errs <- err
		}
		go read()
		synctest.Wait() // the first read is making the reader
		go read()
		synctest.Wait() // the second has done all it can before it is made
		close(release)
		for range 2 {
			if err := <-errs; err != nil {
				t.Errorf("a read while its reader was being made: %v", err)
			}
		}
		if d.Started != 2 {
			t.Fatalf("started %d readers, want the first and its one remake", d.Started)
		}
	})
}

// A reader removed between being found running and the script running in
// it is made again once and the read answers.
//
// Kills: "No such container" from the exec taken as the read's failure.
func TestAReaderRemovedUnderAReadIsMadeAgainOnce(t *testing.T) {
	t.Parallel()
	d := standingDaemon()
	c := &creating{d: d, made: map[string]bool{}}
	spec := containerresult.Spec{Image: "busybox@sha256:1", Script: "read"}
	if _, err := containerresult.RunStanding(t.Context(), c.dk, "race-gone", spec); err != nil {
		t.Fatal(err)
	}
	name := readerName(t, d)
	var once sync.Once
	c.before = func(args []string) {
		if args[0] == "exec" && strings.Contains(strings.Join(args, " "), "sh -c") {
			once.Do(func() {
				d.StopReader(name, true)
				c.mu.Lock()
				delete(c.made, name)
				c.mu.Unlock()
			})
		}
	}
	got, err := containerresult.RunStanding(t.Context(), c.dk, "race-gone", spec)
	if err != nil || string(got) != "read busybox@sha256:1" {
		t.Fatalf("a read whose reader was removed under it: %q, %v", got, err)
	}
	if d.Started != 2 || readerName(t, d) != name {
		t.Fatalf("started %d, readers %v; want the reader made again once", d.Started, d.Readers())
	}
}

// A new image's reader leaves the old image's while a read is running in
// it, and removes it on a later read once it is idle.
//
// Kills: a replaced reader removed from under a read that is using it.
func TestAReplacedReaderIsRemovedOnlyWhenIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := standingDaemon()
		c := &creating{d: d, made: map[string]bool{}}
		class := newClass("race-replace")
		old := containerresult.Spec{Image: "busybox@sha256:1", Script: "read"}
		if _, err := containerresult.RunStanding(t.Context(), c.dk, class, old); err != nil {
			t.Fatal(err)
		}
		oldName := readerName(t, d)
		inRead, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		c.before = func(args []string) {
			if args[0] == "exec" && strings.Contains(strings.Join(args, " "), oldName+" sh -c") {
				once.Do(func() { close(inRead); <-release })
			}
		}
		oldRead := make(chan error, 1)
		go func() {
			_, err := containerresult.RunStanding(t.Context(), c.dk, class, old)
			oldRead <- err
		}()
		<-inRead
		next := containerresult.Spec{Image: "busybox@sha256:2", Script: "read"}
		if _, err := containerresult.RunStanding(t.Context(), c.dk, class, next); err != nil {
			t.Fatal(err)
		}
		if got := d.Readers(); len(got) != 2 {
			t.Errorf("readers %v while the old one is read, want both", got)
		}
		close(release)
		if err := <-oldRead; err != nil {
			t.Fatalf("the old reader's read: %v", err)
		}
		if _, err := containerresult.RunStanding(t.Context(), c.dk, class, next); err != nil {
			t.Fatal(err)
		}
		if got := readerName(t, d); got == oldName {
			t.Fatalf("readers %v, want only the new image's", d.Readers())
		}
	})
}
