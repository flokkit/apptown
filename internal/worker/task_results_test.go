package worker_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"hash"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
	"github.com/flokkit/apptown/stock"
)

// counted hashes and counts what it is given, keeping none of it.
type counted struct {
	n   int64
	sum hash.Hash
}

func (c *counted) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return c.sum.Write(p)
}

// A result larger than one answer could ever carry (over the old 96 MiB a
// task's results could be together) comes back whole through the worker as
// a stream to the caller's writer: every byte arrives, and neither the
// worker nor AppTown (both in this process) allocates anything near its
// size to capture and pass it on. A result over its declared Max is too
// large with its size, and has no copy to read; once the task is released
// its results are gone. (A fake backend: the file is written where the
// task would leave it.) Not parallel: the allocations measured are the
// process's.
//
// Kills: the 64 MiB per-result and 96 MiB total caps kept (the task is
// refused); a result read whole into memory on either side (the worker's
// capture or serving, or the client's ReadResult); a too-large result
// copied or served.
func TestALargeResultIsStreamedWhole(t *testing.T) {
	ca := workertest.NewCA(t)
	state := t.TempDir()
	srv := workertest.Start(t, ca, state, &executionfake.Tasks{})
	driver := workertest.Driver(t, ca, srv)
	ctx := context.Background()
	spec := execution.TaskSpec{Key: "dump", Image: "fake", Argv: []string{"run"},
		Admission: execution.Admission{Profile: "worker", Network: execution.NetworkDefault, Expires: time.Now().Add(time.Hour)},
		Results: []execution.Result{{Name: "dump", Path: "/apptown/state/dump.sql.gz", Max: 200 << 20},
			{Name: "over", Path: "/apptown/state/over", Max: 4}}}
	created, err := driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	const size = 100<<20 + 1
	own := filepath.Join(state, "task-state", "dump")
	f, err := os.Create(filepath.Join(own, "dump.sql.gz"))
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.New()
	chunk := bytes.Repeat([]byte("0123456789abcdef"), 1<<16)
	for written := 0; written < size; {
		part := chunk[:min(len(chunk), size-written)]
		if _, err := io.MultiWriter(f, want).Write(part); err != nil {
			t.Fatal(err)
		}
		written += len(part)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, "over"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: spec.Key, ProviderID: created.ID}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Wait(ctx, ref); err != nil {
		t.Fatal(err)
	}
	states, err := driver.ResultStates(ctx, ref)
	if err != nil || states["dump"].State != execution.ResultPresent || states["dump"].Size != size {
		t.Fatalf("the captured results: %+v %v", states, err)
	}
	got := &counted{sum: sha256.New()}
	if err := driver.ReadResult(ctx, ref, "dump", size, got); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if got.n != size || !bytes.Equal(got.sum.Sum(nil), want.Sum(nil)) {
		t.Fatalf("read %d bytes, want %d, same bytes %v", got.n, size, bytes.Equal(got.sum.Sum(nil), want.Sum(nil)))
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > size/4 {
		t.Fatalf("capturing and reading a %d-byte result allocated %d bytes", size, allocated)
	}

	if over := states["over"]; over.State != execution.ResultTooLarge || over.Size != 5 {
		t.Errorf("a result over its Max: %+v", over)
	}
	if err := driver.ReadResult(ctx, ref, "over", 5, io.Discard); err == nil {
		t.Error("a too-large result was served")
	}
	if _, err := os.Stat(filepath.Join(state, "task-results", "dump", "results", "over")); !os.IsNotExist(err) {
		t.Errorf("a too-large result was copied: %v", err)
	}
	if err := driver.Release(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if err := driver.ReadResult(ctx, ref, "dump", size, io.Discard); err == nil {
		t.Error("a released task's result was served")
	}
}

// A result is not copied when the copy would leave the worker's state
// folder with less than its floor free: the capture records it refused,
// saying what is free, and nothing is written, as for an image.
//
// Kills: the result capture's disk admission removed.
func TestAResultWhoseCopyWouldFillTheStateFolderIsRefused(t *testing.T) {
	t.Parallel()
	ca := workertest.NewCA(t)
	state := t.TempDir()
	const gib = int64(1) << 30
	var free atomic.Int64
	free.Store(50 * gib)
	srv := workertest.StartServer(t, ca, &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: state, Tasks: &executionfake.Tasks{},
		StateDisk: func(string) (diskspace.Space, error) {
			return diskspace.Space{TotalBytes: 100 * gib, FreeBytes: free.Load()}, nil
		}})
	driver := workertest.Driver(t, ca, srv)
	ctx := context.Background()
	spec := execution.TaskSpec{Key: "dump", Image: "fake", Argv: []string{"run"},
		Admission: execution.Admission{Profile: "worker", Network: execution.NetworkDefault, Expires: time.Now().Add(time.Hour)},
		Results:   []execution.Result{{Name: "dump", Path: "/apptown/state/dump.sql.gz", Max: 1 << 20}}}
	created, err := driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "task-state", "dump", "dump.sql.gz"), []byte("the dump"), 0o644); err != nil {
		t.Fatal(err)
	}
	free.Store(5 * gib) // exactly the floor: any copy goes below it
	ref := execution.TaskRef{Key: spec.Key, ProviderID: created.ID}
	if err := driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	states, err := driver.ResultStates(ctx, ref)
	if err != nil || states["dump"].State != execution.ResultRefused || !strings.Contains(states["dump"].Reason, "5.0 GiB free of 100.0 GiB") {
		t.Fatalf("the captured results: %+v %v", states, err)
	}
	if _, err := os.Stat(filepath.Join(state, "task-results", "dump", "results", "dump")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the refused result was copied: %v", err)
	}
}
