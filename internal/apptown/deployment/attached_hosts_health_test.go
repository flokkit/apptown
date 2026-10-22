package deployment

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// On an AppTown with no Docker of its own, Docker deploys are as healthy as
// the attached Docker hosts they go to: one that answers makes them
// working, naming it; none attached says to attach one.
func TestDockerDeploysWithNoLocalDockerAreTheAttachedHostsHealth(t *testing.T) {
	t.Parallel()
	w := noLocalDockerWorld(t, "app:\n  web: {static: site, public: true}\n")
	s := w.service(true)
	if h := s.AttachedDockerHostsHealth(context.Background()); !h.OK || !strings.Contains(h.Detail, "remote answered") {
		t.Fatalf("an attached host that answers: %+v", h)
	}
	w.daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "info" {
			return []byte("ssh: connect to host 127.0.0.1 port 22: Connection refused"), errors.New("exit status 255"), true
		}
		return nil, nil, false
	})
	if h := s.AttachedDockerHostsHealth(context.Background()); h.OK || !strings.Contains(h.Detail, "none answered: remote") {
		t.Fatalf("an attached host that does not answer: %+v", h)
	}
	if err := s.Targets.Retire(context.Background(), "remote", "test"); err != nil {
		t.Fatal(err)
	}
	if h := s.AttachedDockerHostsHealth(context.Background()); h.OK || !strings.Contains(h.Detail, "none is attached") {
		t.Fatalf("no attached host: %+v", h)
	}
}
