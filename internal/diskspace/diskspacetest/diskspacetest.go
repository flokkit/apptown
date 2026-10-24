// Package diskspacetest helps test stand-ins of a Docker daemon answer a
// disk read through Docker (diskspace.ReadThroughDocker) without knowing
// its steps.
package diskspacetest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/dockerlabel"
)

// Step names the docker call of a disk read that args is (sweep, create,
// start, wait, cp, logs, inspect, rm, and the image inspect and version a
// failed read asks for its evidence), or "" for any other call. A sweep's
// removal of a leftover, by its ID, is not named.
func Step(args []string) string {
	if len(args) == 0 {
		return ""
	}
	switch {
	case args[0] == "ps" && slices.Contains(args, "label="+dockerlabel.Class+"="+diskspace.ProbeClass):
		return "sweep"
	case args[0] == "create" && len(args) > 2 && args[1] == "--name" && strings.HasPrefix(args[2], diskspace.ProbePrefix):
		return "create"
	case args[0] == "cp" && len(args) == 3 && strings.HasPrefix(args[1], diskspace.ProbePrefix):
		return "cp"
	case slices.Equal(args, []string{"version", "--format", "{{json .}}"}):
		return "version"
	case len(args) == 5 && slices.Equal(args[:4], []string{"image", "inspect", "--format", "{{json .}}"}):
		return "image"
	case slices.Contains([]string{"start", "wait", "logs", "inspect", "rm"}, args[0]) && strings.HasPrefix(args[len(args)-1], diskspace.ProbePrefix):
		return args[0]
	}
	return ""
}

// WriteResult answers the read's cp, args, as docker cp would for a probe
// whose df printed out and stderr and exited rc: the result folder's
// files, under the destination folder. It reports whether args is that cp.
func WriteResult(args []string, out, stderr, rc string) (bool, error) {
	if Step(args) != "cp" {
		return false, nil
	}
	dir := filepath.Join(args[2], filepath.Base(diskspace.ResultFolder))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return true, err
	}
	for i, body := range []string{out, stderr, rc + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, diskspace.ResultFiles[i]), []byte(body), 0o644); err != nil {
			return true, err
		}
	}
	return true, nil
}
