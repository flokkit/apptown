package dockerfake

import (
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
)

func TestNamedVolumeExistenceFollowsSuccessfulCreationAndRemoval(t *testing.T) {
	d := newDaemon()
	scope := docker.Scope{Client: d}
	exists := func(want bool) {
		t.Helper()
		got, err := scope.Exists(t.Context(), "volume", "inspect", "retained")
		if err != nil || got != want {
			t.Fatalf("volume existence=%v, %v; want %v", got, err, want)
		}
	}
	exists(false)
	d.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 1 && args[0] == "volume" && args[1] == "create" {
			return nil, errors.New("refused"), true
		}
		return nil, nil, false
	})
	if _, err := scope.Run(t.Context(), "volume", "create", "retained"); err == nil {
		t.Fatal("injected creation failure lost")
	}
	exists(false)
	d.SetReply(nil)
	if _, err := scope.Run(t.Context(), "volume", "create", "retained"); err != nil {
		t.Fatal(err)
	}
	exists(true)
	if _, err := scope.Run(t.Context(), "volume", "rm", "retained"); err != nil {
		t.Fatal(err)
	}
	exists(false)
}

func newDaemon() *Daemon {
	return New()
}

// Installing a disk fake must not enable a different result protocol and
// hide containers recorded by the ordinary daemon state.
func TestUnconfiguredResultsLeaveContainerListingsWithTheDaemon(t *testing.T) {
	t.Parallel()
	scope := docker.Scope{Client: newDaemon()}
	if _, err := scope.Run(t.Context(), "run", "-d", "--name", "leftover", "--label", "apptown.class=container-result", "busybox"); err != nil {
		t.Fatal(err)
	}
	out, err := scope.Run(t.Context(), "ps", "-aq", "--filter", "label=apptown.class=container-result")
	if err != nil || string(out) != "leftover" {
		t.Fatalf("recorded result container disappeared: %q %v", out, err)
	}
}
