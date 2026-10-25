package docker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/diskspace/diskspacetest"
)

type fakeClient struct {
	disk  diskspacetest.Daemon
	hosts []string
	args  [][]string
	out   string
	err   error
	reply func(Command) ([]byte, error)
}

func (f *fakeClient) Run(_ context.Context, host string, req Command) ([]byte, error) {
	f.hosts = append(f.hosts, host)
	f.args = append(f.args, req.Args)
	out, err := []byte(f.out), f.err
	if f.reply != nil {
		out, err = f.reply(req)
	}
	// A fake not scripted for the disk admission's questions answers them
	// as a daemon with room.
	if err == nil && len(strings.TrimSpace(string(out))) == 0 {
		if roomy, diskErr, ok := f.disk.Roomy(req.Args); ok {
			return roomy, diskErr
		}
	}
	return out, err
}

// A scope names the daemon a command talks to: AppTown's own, or an
// attached host's over SSH; the host travels with every command, and a
// scope with no client refuses rather than guessing.
func TestScopeCarriesItsHost(t *testing.T) {
	c := &fakeClient{out: "a\nb\n\nc\n"}
	local := Scope{Client: c}
	if local.Remote() {
		t.Fatal("AppTown's own daemon is not remote")
	}
	remote := local.WithHost("ssh://droplet", "203.0.113.5", "apps.example")
	if !remote.Remote() || remote.Hostname != "203.0.113.5" || remote.Domain != "apps.example" || remote.Client != c {
		t.Fatalf("attached host: %+v", remote)
	}
	if lines := remote.Lines(context.Background(), "ps"); len(lines) != 3 || lines[2] != "c" {
		t.Fatalf("lines drop blanks: %v", lines)
	}
	if !remote.OK(context.Background(), "info") || c.hosts[len(c.hosts)-1] != "ssh://droplet" {
		t.Fatalf("commands go to the scope's host: %v", c.hosts)
	}
	if _, err := (Scope{}).Command(context.Background(), Command{Args: []string{"ps"}}); err == nil {
		t.Fatal("a scope without a client refuses")
	}
	c.err = errors.New("daemon down")
	if remote.OK(context.Background(), "info") || remote.Text(context.Background(), "info") != "" {
		t.Fatal("a failing daemon answers false and empty")
	}
	if got := strings.Join(c.args[0], " "); got != "ps" {
		t.Fatalf("args: %q", got)
	}
}

// Only the daemon's own "no such" answer is absence, wherever in its output
// it comes (`docker inspect` prints "[]" first); anything else that fails
// is not knowing, never "gone".
func TestExistsTellsAbsenceFromNotKnowing(t *testing.T) {
	for _, c := range []struct {
		out   string
		err   error
		there bool
		fails bool
	}{
		{out: "[{}]", there: true},
		{out: "[]\nerror: no such object: abc\n", err: errors.New("exit status 1")},
		{out: "Error response from daemon: get v: no such volume\n", err: errors.New("exit status 1")},
		{out: "error during connect: connection reset by peer", err: errors.New("exit status 1"), fails: true},
		{out: "[]\n", err: errors.New("exit status 1"), fails: true},
	} {
		there, err := Scope{Client: &fakeClient{out: c.out, err: c.err}}.Exists(context.Background(), "inspect", "abc")
		if there != c.there || (err != nil) != c.fails {
			t.Errorf("%q: there=%v err=%v", c.out, there, err)
		}
	}
}
