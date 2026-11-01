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
	_, err := containerresult.RunStanding(ctx, func(ctx context.Context, _ []string, args ...string) ([]byte, error) {
		out, stderr, err := dk(ctx, args...)
		if err != nil {
			return []byte(out + stderr), err
		}
		if len(args) > 0 && args[0] == "logs" {
			return []byte(out + stderr), nil
		}
		return []byte(out), nil
	}, ReaderClass, containerresult.Spec{
		Image: image, Options: []string{"--network", "none", "-v", root + ":/d:ro"},
		Script: "df -Pk /d", Now: now,
		Validate: func(answer []byte) error { var err error; space, err = parseDF(answer); return err },
	})
	if err != nil {
		return Space{}, err
	}
	return space, nil
}
