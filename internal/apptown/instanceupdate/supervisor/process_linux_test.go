//go:build linux

package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/flokkit/apptown/internal/execfile"
	"github.com/flokkit/apptown/internal/execution"
)

func init() {
	// The helper's main goroutine stays on the leader thread while testing runs
	// the helper test on another thread. This reproduces ordinary Go scheduling.
	if os.Getenv("APPTOWN_U2_PROCESS_HELPER") == "guard" {
		runtime.LockOSThread()
	}
}
func TestNonLeaderGuardChildIsObservedAndAuthenticates(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out := (execution.Runner{}).Run(ctx, execution.Local, execution.Request{Argv: []string{binary, "-test.run=^TestU2GuardProcessHelper$", "-test.v"}, Env: []string{"APPTOWN_U2_PROCESS_HELPER=guard"}, Timeout: 15 * time.Second})
	output, err := out.Output, out.RunErr
	if err != nil {
		t.Fatalf("non-leader guard child failed: %v\n%s", err, output)
	}
}
func TestU2GuardProcessHelper(t *testing.T) {
	if os.Getenv("APPTOWN_U2_PROCESS_HELPER") != "guard" {
		t.Skip("separate process fixture")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if unix.Gettid() == os.Getpid() {
		t.Fatal("fixture did not obtain a non-leader launch thread")
	}
	s := testStore(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(root, "bin", "apptownd")
	body, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := execfile.Write(childPath, body, 0700); err != nil {
		t.Fatal(err)
	}

	r := s.registration
	r.Supervisor = binary
	r.ReleaseDir = root
	r.DaemonUID = uint32(os.Geteuid())
	s.registration.DaemonUID = r.DaemonUID
	socket, _, _ := startControl(t, s, observeFunc(func(context.Context, Registration) (Observation, error) { return managedChild(os.Getpid(), r) }))
	in := intention(s)
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	env := []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "APPTOWN_U2_PROCESS_HELPER=") {
			env = append(env, value)
		}
	}
	env = append(env, "APPTOWN_U2_PROCESS_HELPER=daemon", "APPTOWN_U2_SOCKET="+socket, "APPTOWN_U2_INTENTION="+string(raw))
	out := (execution.Runner{}).Run(ctx, execution.Local, execution.Request{Argv: []string{childPath, "-test.run=^TestU2DaemonPeerHelper$", "-test.v"}, Env: env, Timeout: 8 * time.Second})
	output, err := out.Output, out.RunErr
	if err != nil {
		t.Fatalf("actual daemon peer failed: %v\n%s", err, output)
	}
	tx, err := s.Transactions()
	if err != nil || len(tx) != 1 || tx[0].Intention != in {
		t.Fatalf("actual peer did not persist exact intention: %+v %v", tx, err)
	}
}
func TestU2DaemonPeerHelper(t *testing.T) {
	if os.Getenv("APPTOWN_U2_PROCESS_HELPER") != "daemon" {
		t.Skip("separate process fixture")
	}
	var in Intention
	if err := json.Unmarshal([]byte(os.Getenv("APPTOWN_U2_INTENTION")), &in); err != nil {
		t.Fatal(err)
	}
	reply, err := Call(context.Background(), os.Getenv("APPTOWN_U2_SOCKET"), Request{Protocol: Protocol, Operation: "stage", Intention: &in})
	if err != nil || reply.Transaction == nil || reply.Transaction.Intention != in {
		t.Fatalf("real observed child refused: %+v %v", reply, err)
	}
}
