// Package execfile writes a file that is about to be executed: a fake
// docker or git a test puts on PATH, a hook, a helper script.
//
// os.WriteFile is not safe for that in a program that starts processes from
// several goroutines, as every parallel test binary does. Between the open
// and the close of the file being written, another goroutine may fork a
// child. The child inherits the write descriptor and holds it until its own
// exec closes it (O_CLOEXEC acts only at exec). If our exec of the new file
// lands in that gap, Linux refuses it with ETXTBSY ("text file busy"),
// because the file is still open for writing somewhere. This is Go issue
// 22315; the standard library has no fix, and it shows up as a rare failure
// on Linux CI that macOS seldom reproduces.
//
// Write holds syscall.ForkLock for reading while the file is open. The
// standard library takes it for writing around every fork (os.StartProcess,
// exec.Cmd). A fork that started earlier cannot inherit a descriptor not yet
// opened, and a later one cannot start until the file is closed.
//
// It does not help with processes started some other way (cgo, raw clone),
// and it is no use for a file written by a different process.
package execfile

import (
	"io/fs"
	"os"
	"syscall"
)

// Write writes data to the named file, creating it with perm (before the
// umask) or truncating it, exactly as os.WriteFile does, with no fork in
// this process while the file is open for writing. Use it for every file
// this process, or a child of it, may execute.
func Write(name string, data []byte, perm fs.FileMode) error {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	return os.WriteFile(name, data, perm)
}
