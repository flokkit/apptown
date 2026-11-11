package diskspace

import (
	"context"
	"fmt"
	"slices"
	"strings"
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
//
// plain says the daemon carries only plain requests, not attached streams
// (Portainer's Docker proxy): the same reader is then made and read by
// plain requests (containerresult.Spec.Plain).
func ReadThroughDocker(ctx context.Context, dk docker, image, root string, now func() time.Time, plain bool) (Space, error) {
	if image == "" {
		return Space{}, fmt.Errorf("no df image: the tool versions name it")
	}
	var space Space
	_, err := readStanding(ctx, dk, image, root, "df -Pk /d", now, plain, func(answer []byte) error { var err error; space, err = parseDF(answer); return err })
	return space, err
}

// ReadKernel reads both host CPU samples and memory in one standing-reader exec.
// Uptime is sampled on the Docker host so transport latency cannot change the
// cgroup CPU rate. The reader identity and options are shared with disk reads,
// and plain is as for ReadThroughDocker.
func ReadKernel(ctx context.Context, dk docker, image, root string, now func() time.Time, plain bool) (map[string][]byte, error) {
	answer, err := readStanding(ctx, dk, image, root, kernelScript, now, plain, nil)
	if err != nil {
		return nil, err
	}
	return parseKernel(answer)
}

// A failed counter read leaves an empty section for its resource parser; the
// other resource can still be known. A failed sampling wait fails the command.
const kernelScript = `section() { echo "== $1"; cat "$2"; }
echo '== scope'
if [ -f /sys/fs/cgroup/memory.current ]; then scope=cgroup
elif [ -f /sys/fs/cgroup/cgroup.controllers ]; then scope=proc
else scope=v1
fi
echo "$scope"
section uptime.first /proc/uptime
case "$scope" in
cgroup)
 section memory.current /sys/fs/cgroup/memory.current
 section memory.stat /sys/fs/cgroup/memory.stat
 section memory.max /sys/fs/cgroup/memory.max
 section cpu.first /sys/fs/cgroup/cpu.stat
 ;;
proc)
 section meminfo /proc/meminfo
 section cpu.first /proc/stat
 ;;
esac
sleep 1 || exit 1
section uptime.second /proc/uptime
case "$scope" in
cgroup) section cpu.second /sys/fs/cgroup/cpu.stat ;;
proc) section cpu.second /proc/stat ;;
esac
exit 0`

func parseKernel(answer []byte) (map[string][]byte, error) {
	parts := map[string][]byte{}
	name := ""
	for _, line := range strings.Split(strings.TrimSuffix(string(answer), "\n"), "\n") {
		if strings.HasPrefix(line, "== ") {
			name = strings.TrimPrefix(line, "== ")
			if _, exists := parts[name]; exists {
				return nil, fmt.Errorf("duplicate kernel section %q", name)
			}
			parts[name] = nil
		} else {
			if name == "" {
				return nil, fmt.Errorf("kernel reading has data outside a section")
			}
			parts[name] = append(parts[name], []byte(line+"\n")...)
		}
	}
	expected := []string{"scope", "uptime.first", "uptime.second"}
	switch strings.TrimSpace(string(parts["scope"])) {
	case "cgroup":
		expected = append(expected, "memory.current", "memory.stat", "memory.max", "cpu.first", "cpu.second")
	case "proc":
		expected = append(expected, "meminfo", "cpu.first", "cpu.second")
	case "v1":
	default:
		return nil, fmt.Errorf("the reader's cgroup scope is not readable")
	}
	for _, name := range expected {
		if _, exists := parts[name]; !exists {
			return nil, fmt.Errorf("missing kernel section %q", name)
		}
	}
	for name := range parts {
		if !slices.Contains(expected, name) {
			return nil, fmt.Errorf("unknown kernel section %q", name)
		}
	}
	return parts, nil
}

func readStanding(ctx context.Context, dk docker, image, root, script string, now func() time.Time, plain bool, validate func([]byte) error) ([]byte, error) {
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
		Validate: validate, Plain: plain,
	})
}
