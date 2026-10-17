//go:build unix

package console

import "syscall"

// openNoWait opens a pipe without waiting for its other end.
const openNoWait = syscall.O_NONBLOCK
