//go:build !linux

package supervisor

import (
	"errors"
	"net"
)

type peerIdentity struct {
	PID int
	UID uint32
}

func peer(*net.UnixConn) (peerIdentity, error) {
	return peerIdentity{}, errors.New("managed supervisor peer authentication requires Linux")
}
