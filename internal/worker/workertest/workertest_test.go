package workertest

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A worker that exits before it listens fails its start as soon as it has
// exited, with its output, instead of waiting out the hang guard.
func TestAWorkerThatExitsBeforeListeningFailsFastWithItsOutput(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close() // nothing listens here
	w := &Container{t: t, host: true, binary: "/bin/sh", URL: "https://" + addr, log: filepath.Join(t.TempDir(), "worker.log"),
		args: []string{"-c", "echo the worker refused its flags; exit 3"}}
	t.Cleanup(w.stopProcess)
	began := time.Now()
	err = w.launch()
	if err == nil {
		t.Fatal("a worker that exited was taken as listening")
	}
	if !strings.Contains(err.Error(), "exited before it listened") || !strings.Contains(err.Error(), "the worker refused its flags") {
		t.Fatalf("the failure does not say the worker exited, with its output: %v", err)
	}
	if took := time.Since(began); took > listenGuard/10 {
		t.Fatalf("the start took %s to notice the worker had exited", took)
	}
}

// A test's worker reads the fixed Disk for its state folder, never the disk
// of the machine the tests run on: whether a test's work is admitted must
// not depend on how full that machine is (soak finding 54).
//
// Kills: serve leaving StateDisk nil, so the worker reads the real disk.
func TestATestWorkerNeverReadsTheMachinesDisk(t *testing.T) {
	t.Parallel()
	ca, state := NewCA(t), t.TempDir()
	got, err := Driver(t, ca, Start(t, ca, state, nil)).Disks(t.Context())
	want, _ := Disk(state)
	if err != nil || len(got) != 1 || got[0].Path != state || got[0].TotalBytes != want.TotalBytes || got[0].FreeBytes != want.FreeBytes || got[0].Refuses {
		t.Fatalf("the test worker's disks: %+v %v", got, err)
	}
}
