package worker

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/stock"
)

// A declared image is captured as a result file is read: a folder of
// regular files in the workspace's state, reached and holding no link,
// within its admitted size. Each other case is its own state with nothing
// copied: absent, refused with its reason (a link at, on the way to or
// inside the folder, a file where the folder should be, a pipe inside it),
// or too large with its size. What is copied is every file, byte for byte.
func TestAnImageIsCapturedConfined(t *testing.T) {
	t.Parallel()
	// A roomy state folder, as workertest.Disk gives (not imported here:
	// workertest imports this package), never the machine's own disk. The
	// capture's disk refusal is
	// TestAnImageWhoseCopyWouldFillTheStateFolderIsRefused's.
	s := &Server{ToolVersions: stock.Versions(), State: t.TempDir(), StateDisk: func(string) (diskspace.Space, error) {
		return diskspace.Space{TotalBytes: 100 << 30, FreeBytes: 50 << 30}, nil
	}}
	state := s.statePath("ws")
	outside := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	layout := func(dir string) {
		write(filepath.Join(dir, "oci-layout"), `{"imageLayoutVersion":"1.0.0"}`)
		write(filepath.Join(dir, "index.json"), `{"manifests":[]}`)
		write(filepath.Join(dir, "blobs", "sha256", strings.Repeat("a", 64)), "layer")
	}
	layout(filepath.Join(state, "out", "good"))
	layout(filepath.Join(state, "out", "big"))
	layout(filepath.Join(state, "out", "inner-link"))
	link(filepath.Join(outside, "secret"), filepath.Join(state, "out", "inner-link", "blobs", "sha256", strings.Repeat("c", 64)))
	write(filepath.Join(outside, "secret"), "secret")
	layout(filepath.Join(outside, "elsewhere"))
	link(filepath.Join(outside, "elsewhere"), filepath.Join(state, "out", "linked"))
	link(filepath.Join(outside), filepath.Join(state, "via"))
	write(filepath.Join(state, "out", "file"), "a file, not a folder")
	layout(filepath.Join(state, "out", "pipe"))
	if err := syscall.Mkfifo(filepath.Join(state, "out", "pipe", "blobs", "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	size := int64(len(`{"imageLayoutVersion":"1.0.0"}`) + len(`{"manifests":[]}`) + len("layer"))

	cases := []struct {
		path string
		max  int64
		want execution.ImageCapture
	}{
		{"/apptown/state/out/good", size, execution.ImageCapture{State: execution.ResultPresent, Size: size}},
		{"/apptown/state/out/nothing", size, execution.ImageCapture{State: execution.ResultAbsent}},
		{"/apptown/state/out/file/under", size, execution.ImageCapture{State: execution.ResultAbsent}},
		{"/apptown/state/out/big", size - 1, execution.ImageCapture{State: execution.ResultTooLarge, Size: size}},
		{"/apptown/state/out/inner-link", 1 << 20, execution.ImageCapture{State: execution.ResultRefused, Reason: execution.RefusedSymlink}},
		{"/apptown/state/out/linked", 1 << 20, execution.ImageCapture{State: execution.ResultRefused, Reason: execution.RefusedSymlink}},
		{"/apptown/state/via/elsewhere", 1 << 20, execution.ImageCapture{State: execution.ResultRefused, Reason: execution.RefusedSymlink}},
		{"/apptown/state/out/file", 1 << 20, execution.ImageCapture{State: execution.ResultRefused, Reason: execution.RefusedNotFolder}},
		{"/apptown/state/out/pipe", 1 << 20, execution.ImageCapture{State: execution.ResultRefused, Reason: execution.RefusedNotRegular}},
	}
	for i, c := range cases {
		dest := filepath.Join(t.TempDir(), "copy")
		got, err := s.captureImage("ws", execution.ImageResult{Name: "img", Path: c.path, Max: c.max}, dest)
		if err != nil {
			t.Errorf("%s: %v", c.path, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: %+v, want %+v", c.path, got, c.want)
		}
		if err := got.Valid(); err != nil {
			t.Errorf("%s: %v", c.path, err)
		}
		_, statErr := os.Lstat(dest)
		if i > 0 && !os.IsNotExist(statErr) {
			t.Errorf("%s: something was copied for a capture that is %s: %v", c.path, got.State, statErr)
		}
	}
	// What was copied is every file of the layout, as it was.
	dest := filepath.Join(t.TempDir(), "copy")
	if _, err := s.captureImage("ws", execution.ImageResult{Name: "img", Path: "/apptown/state/out/good", Max: size}, dest); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"oci-layout", "index.json", filepath.Join("blobs", "sha256", strings.Repeat("a", 64))} {
		want, _ := os.ReadFile(filepath.Join(state, "out", "good", rel))
		if got, err := os.ReadFile(filepath.Join(dest, rel)); err != nil || string(got) != string(want) {
			t.Errorf("the copy of %s: %q %v, want %q", rel, got, err, want)
		}
	}
}
