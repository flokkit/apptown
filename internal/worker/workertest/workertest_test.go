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
