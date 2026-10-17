package dbtest

import (
	"context"
	"net"
	"testing"

	"golang.org/x/sys/unix"
)

// The harness's connections close by reset (SO_LINGER 0), so a closed test
// connection does not hold one of the machine's local ports for 30 s.
func TestTheHarnessDialsConnectionsThatCloseWithoutWaiting(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	conn, err := closeWithoutWaiting(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	raw, err := conn.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var linger *unix.Linger
	var sockErr error
	if err := raw.Control(func(fd uintptr) { linger, sockErr = unix.GetsockoptLinger(int(fd), unix.SOL_SOCKET, unix.SO_LINGER) }); err != nil || sockErr != nil {
		t.Fatal(err, sockErr)
	}
	if linger.Onoff == 0 || linger.Linger != 0 {
		t.Fatalf("SO_LINGER is %+v, want on with no wait", linger)
	}
}
