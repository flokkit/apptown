package diskspace_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/diskspace"
)

func TestDiskReadValidatesTheCopiedAnswer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		answer []byte
		code   int
		valid  bool
	}{
		{"roomy", []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x 1000 100 900 10% /d\n"), 0, true},
		{"empty", []byte{}, 0, false}, {"missing", nil, 0, false},
		{"malformed", []byte("Filesystem\n/dev/x lots\n"), 0, false},
		{"failed", []byte("partial"), 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &containerresulttest.Daemon{Answers: func(c containerresulttest.Command) (containerresulttest.Answer, bool) {
				if c.Script != "df -Pk /d" {
					return containerresulttest.Answer{}, false
				}
				if c.Image != "chosen-image" || !slices.Contains(c.Args, "/var/lib/docker:/d:ro") {
					t.Fatalf("wrong image/mount: %+v", c)
				}
				network := slices.Index(c.Args, "--network")
				if network < 0 || c.Args[network+1] != "none" {
					t.Fatalf("network not isolated: %+v", c)
				}
				return containerresulttest.Answer{Answer: tc.answer, Code: tc.code}, true
			}}
			space, err := diskspace.ReadThroughDocker(t.Context(), func(_ context.Context, args ...string) (string, string, error) {
				out, err, ok := d.Run(args)
				if !ok {
					return "", "", fmt.Errorf("unexpected diagnostic command %v", args)
				}
				return string(out), "", err
			}, "chosen-image", "/var/lib/docker", time.Now)
			if (err == nil) != tc.valid {
				t.Fatalf("space=%+v error=%v valid=%v", space, err, tc.valid)
			}
			if tc.valid && (space.TotalBytes != 1000*1024 || space.FreeBytes != 900*1024) {
				t.Fatalf("wrong copied space: %+v", space)
			}
			if d.Removed != 1 {
				t.Fatalf("owned container removed %d times", d.Removed)
			}
		})
	}
}
