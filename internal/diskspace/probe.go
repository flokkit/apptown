package diskspace

import (
	"context"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/containerresult"
)

// docker calls the selected daemon, retaining stdout and stderr separately.
type docker func(context.Context, ...string) (stdout, stderr string, err error)

// ReaderClass is the apptown.class of a daemon's standing disk reader.
const ReaderClass = "disk-reader"

// ReadThroughDocker reads the filesystem holding root through a read-only
// mount in the caller's snapshotted df image, in the daemon's standing disk
// reader (containerresult.RunStanding): readings recur for as long as a page
// is open, and each through a container of its own cost the host a container
// start. The answer is copied from a file and validated. Unknown or
// malformed answers refuse disk admission; logs are never answers.
func ReadThroughDocker(ctx context.Context, dk docker, image, root string, now func() time.Time) (Space, error) {
	if image == "" {
		return Space{}, fmt.Errorf("no df image: the tool versions name it")
	}
	var space Space
	_, err := readStanding(ctx, dk, image, root, "df -Pk /d", now, func(answer []byte) error { var err error; space, err = parseDF(answer); return err })
	return space, err
}

// ReadKernel reads host counters through the same standing reader as disk.
// scope identifies a cgroup v2 slice, a whole-machine root, or unsupported v1.
// Only the capacity files below are admitted; the data root and options keep
// the reader's identity identical to disk's, so there is one per daemon.
func ReadKernel(ctx context.Context, dk docker, image, root, file string, now func() time.Time) ([]byte, error) {
	var script string
	switch file {
	case "scope":
		script = "if [ -f /sys/fs/cgroup/memory.current ]; then echo cgroup; elif [ -f /sys/fs/cgroup/cgroup.controllers ]; then echo proc; else echo v1; fi"
	case "meminfo", "stat":
		script = "cat /proc/" + file
	case "memory.current", "memory.stat", "memory.max", "cpu.stat":
		script = "cat /sys/fs/cgroup/" + file
	default:
		return nil, fmt.Errorf("unsupported kernel reading %q", file)
	}
	return readStanding(ctx, dk, image, root, script, now, nil)
}

func readStanding(ctx context.Context, dk docker, image, root, script string, now func() time.Time, validate func([]byte) error) ([]byte, error) {
	return containerresult.RunStanding(ctx, func(ctx context.Context, _ []string, args ...string) ([]byte, error) {
		out, stderr, err := dk(ctx, args...)
		if err != nil {
			return []byte(out + stderr), err
		}
		if len(args) > 0 && args[0] == "logs" {
			return []byte(out + stderr), nil
		}
		return []byte(out), nil
	}, ReaderClass, containerresult.Spec{
		Image: image, Options: []string{"--network", "none", "--cgroupns", "host", "-v", root + ":/d:ro"},
		Script: script, Now: now,
		Validate: validate,
	})
}
