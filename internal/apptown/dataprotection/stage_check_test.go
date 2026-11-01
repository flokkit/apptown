package dataprotection

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// A copy staged on a rehearsal host is held there to the set's record: the
// receiving container is given the recorded size and digest by name and
// checks what it wrote, so a copy whose stream fell short on its way in is
// refused, not rehearsed.
//
// Kills: a staging that trusts its stdin arrived whole.
func TestACopyStagedOnTheHostIsHeldToTheSetsRecord(t *testing.T) {
	t.Parallel()
	it := &BackupItem{File: "postgres.dump", Bytes: 4, SHA256: "aaaa"}
	read := func(_ context.Context, _ *BackupItem, w io.Writer) (int64, string, error) {
		_, err := io.WriteString(w, "copy")
		return 4, "aaaa", err
	}
	for _, short := range []bool{false, true} {
		d := newDockerFake()
		var env []string
		d.SetCommandReply(func(c docker.Command) ([]byte, error, bool) {
			if c.Args[0] != "run" {
				return nil, nil, false
			}
			_, _ = io.Copy(io.Discard, c.Stdin)
			env = c.Env
			if short {
				return nil, errors.New("exit status 3"), true // the receiver's sum did not match
			}
			return nil, nil, true
		})
		s := &Service{core: &core{env: &deployment.Environment{}}}
		err := s.stageOnHost(context.Background(), docker.Scope{Client: d}, read, it, "rehearsal-copy", "test-helper")
		if !slices.Equal(env, []string{"APPTOWN_COPY_BYTES=4", "APPTOWN_COPY_SHA256=aaaa"}) {
			t.Fatalf("the receiver was given %v; want the set's size and digest", env)
		}
		if short != (err != nil && strings.Contains(err.Error(), "as the set records")) {
			t.Fatalf("short %v: got %v", short, err)
		}
	}
}
