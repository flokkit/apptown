package diskspace_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/diskspace/diskspacetest"
)

func readerDaemon() *containerresulttest.Daemon {
	return &containerresulttest.Daemon{Answers: func(c containerresulttest.Command) (containerresulttest.Answer, bool) {
		return containerresulttest.Answer{Answer: []byte(diskspacetest.RoomyAnswer)}, c.Script == "df -Pk /d"
	}}
}

func read(t *testing.T, d *containerresulttest.Daemon, image string) error {
	t.Helper()
	_, err := diskspace.ReadThroughDocker(t.Context(), func(_ context.Context, args ...string) (string, string, error) {
		out, err, ok := d.Run(args)
		if !ok {
			return "", "", fmt.Errorf("unexpected command %v", args)
		}
		return string(out), "", err
	}, image, "/var/lib/docker", time.Now)
	return err
}

// An hour of an open page's disk readings of one Docker host (a worker's,
// once a minute, and a target's, each five minutes) starts one container:
// the host's standing reader. Each container start costs the host's init a
// scope, a network interface and mounts; live's CT lost its systemd to
// memory after weeks of a throwaway container per reading.
//
// Kills: a reading through a container of its own.
func TestAnHourOfDiskReadingsStartsOneContainerPerHost(t *testing.T) {
	t.Parallel()
	d := readerDaemon()
	for i := range 72 {
		if err := read(t, d, "busybox@sha256:1"); err != nil {
			t.Fatalf("reading %d: %v", i, err)
		}
	}
	if d.Started != 1 || len(d.Readers()) != 1 {
		t.Fatalf("72 readings started %d containers and left readers %v, want one standing reader", d.Started, d.Readers())
	}
}

// A reader that is gone is made again once; one that stopped is started,
// not replaced; a new df image replaces the old image's reader.
//
// Kills: a reader made again for every reading, or one left beside its
// replacement.
//
// Not parallel: a replaced reader is kept while any read in the process
// uses one of its name (containerresult.RunStanding), and the other tests
// here read the same image's reader.
func TestTheStandingReaderIsMadeAgainOnlyWhenGone(t *testing.T) {
	d := readerDaemon()
	if err := read(t, d, "busybox@sha256:1"); err != nil {
		t.Fatal(err)
	}
	first := d.Readers()[0]
	d.StopReader(first, false)
	if err := read(t, d, "busybox@sha256:1"); err != nil || d.Started != 2 || d.Readers()[0] != first {
		t.Fatalf("a stopped reader: err %v, started %d, readers %v; want it started again", err, d.Started, d.Readers())
	}
	d.StopReader(first, true)
	for range 3 {
		if err := read(t, d, "busybox@sha256:1"); err != nil {
			t.Fatal(err)
		}
	}
	if d.Started != 3 || len(d.Readers()) != 1 {
		t.Fatalf("a reader gone: started %d, readers %v; want one made again", d.Started, d.Readers())
	}
	if err := read(t, d, "busybox@sha256:2"); err != nil {
		t.Fatal(err)
	}
	if got := d.Readers(); len(got) != 1 || got[0] == first {
		t.Fatalf("a new image's reader: readers %v, want only the new one", got)
	}
}
