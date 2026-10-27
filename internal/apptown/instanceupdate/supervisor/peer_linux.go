//go:build linux

package supervisor

import (
	"golang.org/x/sys/unix"
	"net"
)

type peerIdentity struct {
	PID int
	UID uint32
}

func peer(c *net.UnixConn) (peerIdentity, error) {
	var out peerIdentity
	raw, err := c.SyscallConn()
	if err != nil {
		return out, err
	}
	var observation error
	err = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		observation = e
		if e == nil {
			out = peerIdentity{PID: int(cred.Pid), UID: cred.Uid}
		}
	})
	if err != nil {
		return out, err
	}
	return out, observation
}
