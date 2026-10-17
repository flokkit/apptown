//go:build unix

package console

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// The agent can replace a listed record
// with a pipe before it is read. The reader rejects it without waiting for
// the pipe's other end.
func TestARecordReplacedByAPipeDoesNotBlockTheReader(t *testing.T) {
	dir, path := writeRecord(t, record)
	files := (&Transcript{Dir: dir}).SessionFiles()
	if len(files) != 1 || files[0] != path {
		t.Fatalf("files: %v", files)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := (&Reader{Path: files[0]}).Next()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a pipe was read as a record")
		}
	case <-time.After(2 * time.Second):
		// Release the waiting open so the test can end.
		fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NONBLOCK, 0o600)
		if err == nil {
			defer syscall.Close(fd)
			<-done
		}
		t.Fatal("the reader waited on a pipe")
	}
}
