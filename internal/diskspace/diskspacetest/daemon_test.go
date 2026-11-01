package diskspacetest_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/containerresult"
	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/diskspace/diskspacetest"
)

func TestDiskFakeLeavesOtherResultCommandsWithTheirOwner(t *testing.T) {
	t.Parallel()
	disk := &diskDaemon{}
	disk.Answer = func() (int, []byte, string) { return 0, []byte("disk"), "" }
	other := &containerresulttest.Daemon{Answers: func(c containerresulttest.Command) (containerresulttest.Answer, bool) {
		return containerresulttest.Answer{Answer: []byte("other")}, c.Script == "other command"
	}}
	dk := func(_ context.Context, _ []string, args ...string) ([]byte, error) {
		if out, err, ok := disk.Run(args); ok {
			return out, err
		}
		if out, err, ok := other.Run(args); ok {
			return out, err
		}
		return nil, fmt.Errorf("unexpected command %v", args)
	}
	for _, tc := range []struct{ script, want string }{{"df -Pk /d", "disk"}, {"other command", "other"}, {"df -Pk /d", "disk"}} {
		read := containerresult.Run
		if tc.want == "disk" {
			read = func(ctx context.Context, dk containerresult.Docker, s containerresult.Spec) ([]byte, error) {
				return containerresult.RunStanding(ctx, dk, diskspace.ReaderClass, s)
			}
		}
		got, err := read(t.Context(), dk, containerresult.Spec{Image: "busybox", Script: tc.script})
		if err != nil || string(got) != tc.want {
			t.Fatalf("%s got %q, %v", tc.script, got, err)
		}
	}
	if other.Removed != 1 {
		t.Fatalf("other owner lost cleanup: %d", other.Removed)
	}
}

// diskDaemon composes the disk and result protocols for this package's tests.
type diskDaemon = diskspacetest.Daemon
