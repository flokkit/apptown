//go:build linux

package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/poll"
)

type observeFunc func(context.Context, Registration) (Observation, error)

func (f observeFunc) Observe(ctx context.Context, r Registration) (Observation, error) {
	return f(ctx, r)
}

func startControl(t *testing.T, s *Store, observe Observer) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (Server{Store: s, Observer: observe}).Serve(ctx) }()
	socket := filepath.Join(s.registration.StateDir, "control.sock")
	if !poll.Until(ctx, 3*time.Second, time.Millisecond, func() bool {
		if st, err := os.Stat(socket); err == nil && st.Mode()&os.ModeSocket != 0 {
			return true
		}
		select {
		case err := <-done:
			t.Fatal(err)
		default:
		}
		return false
	}) {
		cancel()
		t.Fatal("control socket did not open")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Error("control server did not settle")
		}
	})
	return socket, cancel, done
}
func TestKernelPeerMustMatchObservedDaemonForProductAttribution(t *testing.T) {
	s := testStore(t)
	s.registration.DaemonUID = uint32(os.Geteuid()) // Default is root; pure control test also supports an unprivileged Linux test user.
	var observed atomic.Int64
	observed.Store(int64(os.Getpid()))
	socket, _, _ := startControl(t, s, observeFunc(func(context.Context, Registration) (Observation, error) {
		return Observation{PID: int(observed.Load()), UID: uint32(os.Geteuid()), Running: true}, nil
	}))
	in := intention(s)
	got, err := Call(context.Background(), socket, Request{Protocol: Protocol, Operation: "stage", Intention: &in})
	if err != nil || got.Transaction == nil {
		t.Fatalf("actual registered peer refused: %+v %v", got, err)
	}
	if _, err := Call(context.Background(), socket, Request{Protocol: Protocol, Operation: "cancel", Request: in.Request}); err != nil {
		t.Fatal(err)
	}
	observed.Add(1)
	another := intention(s)
	// Refusal may close the socket before the client writes its request.
	// The error spelling is transport-dependent; no journal effect is permitted.
	if _, err := Call(context.Background(), socket, Request{Protocol: Protocol, Operation: "stage", Intention: &another}); err == nil {
		t.Fatalf("another same-UID process attributed a request: %v", err)
	}
	tx, err := s.Transactions()
	if err != nil || len(tx) != 1 {
		t.Fatalf("unauthenticated request mutated journal: %v %v", tx, err)
	}
}
func TestUnavailableProcessObservationDoesNotAuthenticateZeroIdentity(t *testing.T) {
	s := testStore(t)
	socket, _, _ := startControl(t, s, observeFunc(func(context.Context, Registration) (Observation, error) {
		return Observation{}, errors.New("inspection failed")
	}))
	in := intention(s)
	if _, err := Call(context.Background(), socket, Request{Protocol: Protocol, Operation: "stage", Intention: &in}); err == nil {
		t.Fatal("unavailable process authenticated")
	}
	tx, err := s.Transactions()
	if err != nil || len(tx) != 0 {
		t.Fatal("unavailable observation mutated journal", tx, err)
	}
}
func TestClientDisconnectDoesNotUndoCommittedIntention(t *testing.T) {
	s := testStore(t)
	s.registration.DaemonUID = uint32(os.Geteuid())
	socket, _, _ := startControl(t, s, observeFunc(func(context.Context, Registration) (Observation, error) {
		return Observation{PID: os.Getpid(), UID: uint32(os.Geteuid()), Running: true}, nil
	}))
	in := intention(s)
	c, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(Request{Protocol: Protocol, Operation: "stage", Intention: &in})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := Call(context.Background(), socket, Request{Protocol: Protocol, Operation: "stage", Intention: &in})
	if err != nil || got.Transaction == nil {
		t.Fatalf("retry lost request: %v %v", got, err)
	}
	tx, err := s.Transactions()
	if err != nil || len(tx) != 1 || tx[0] != *got.Transaction {
		t.Fatal("retry duplicated request", tx, err)
	}
}
func TestUnknownControlOperationCannotStartOrChangePhase(t *testing.T) {
	s := testStore(t)
	s.registration.DaemonUID = uint32(os.Geteuid())
	socket, _, _ := startControl(t, s, observeFunc(func(context.Context, Registration) (Observation, error) {
		return Observation{PID: os.Getpid(), UID: uint32(os.Geteuid()), Running: true}, nil
	}))
	for _, operation := range []string{"stop", "install", "set-phase", "opening", "recover", "exec"} {
		if _, err := Call(context.Background(), socket, Request{Protocol: Protocol, Operation: operation}); err == nil || !strings.Contains(err.Error(), "unsupported supervisor operation") {
			t.Fatal("accepted consequential operation", operation)
		}
	}
	tx, err := s.Transactions()
	if err != nil || len(tx) != 0 {
		t.Fatal("unsupported operation changed journal", tx, err)
	}
}

func TestUnconfirmedContainerSettlementLeavesDurableStartupRefusal(t *testing.T) {
	s := testStore(t)
	if err := s.retainStartupFailure(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	fresh, err := Open(s.registration.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err := fresh.OrdinaryStart(); err == nil {
		t.Fatal("unconfirmed container settlement allowed another ordinary launch")
	}
	in := intention(fresh)
	if _, err := fresh.Stage(in, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Cancel(in.Request); err != nil {
		t.Fatal(err)
	}
	if err := fresh.OrdinaryStart(); err == nil {
		t.Fatal("cancelling an intention cleared process settlement failure")
	}
}
