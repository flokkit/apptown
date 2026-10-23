package docker

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
)

// Docker's own verdict on a need ends a deployment attempt as a known
// failure: nothing of the app was started. A wait the caller ended stays
// unknown.
func TestANeedDockerCallsUnhealthyIsAKnownFailure(t *testing.T) {
	t.Parallel()
	svcs := []deploy.ServiceSpec{{Name: "postgres", Health: "pg_isready"}}
	daemon := func(status string) Scope {
		return Scope{Client: &fakeClient{reply: func(Command) ([]byte, error) { return []byte(status), nil }}}
	}
	if _, err := waitStackHealthy(context.Background(), daemon("unhealthy"), svcs, "s"); err == nil || !deploy.IsTerminal(err) {
		t.Fatalf("an unhealthy need is not a known failure: %v", err)
	}
	if _, err := waitStackHealthy(context.Background(), daemon("healthy"), svcs, "s"); err != nil {
		t.Fatalf("a healthy need failed the wait: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitStackHealthy(ctx, daemon("starting"), svcs, "s"); err == nil || deploy.IsTerminal(err) {
		t.Fatalf("a wait ended by its caller claims a known outcome: %v", err)
	}
}

// A need the daemon says is not running did not fail a health check after
// it started: the wait says it is not running, a known outcome.
func TestANeedThatIsNotRunningIsSaidSoNotUnhealthy(t *testing.T) {
	t.Parallel()
	svcs := []deploy.ServiceSpec{{Name: "postgres", Health: "pg_isready"}}
	dk := Scope{Client: &fakeClient{reply: func(c Command) ([]byte, error) {
		if slices.Contains(c.Args, "{{.State.Running}}") {
			return []byte("false"), nil
		}
		return []byte("unhealthy"), nil
	}}}
	_, err := waitStackHealthy(context.Background(), dk, svcs, "s")
	if err == nil || !deploy.IsTerminal(err) || !strings.Contains(err.Error(), "not running") || strings.Contains(err.Error(), "after it started") {
		t.Fatalf("a stopped need: %v", err)
	}
}

// Docker reports a container it is restarting under its restart policy as
// unhealthy, because the checks stopped with the process. That is not its
// verdict: the wait goes on, and a need that then passes its check is
// ready. (22 Oct: a first MariaDB start on a busy CI runner gave up its
// setup, exited and was restarted; the gate failed the deployment while
// the database became healthy 36 seconds later.) The restart may also end
// between the gate's two reads: the second read, of both, then says the
// need is starting again, and the first read's unhealthy is not a verdict
// either.
func TestANeedDockerIsRestartingIsNotJudgedUnhealthy(t *testing.T) {
	t.Parallel()
	for name, second := range map[string]string{"still restarting": "true unhealthy", "started again": "false starting"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svcs := []deploy.ServiceSpec{{Name: "mariadb", Health: "healthcheck.sh --connect"}}
			asked := 0
			dk := Scope{Client: &fakeClient{reply: func(c Command) ([]byte, error) {
				switch {
				case slices.Contains(c.Args, "{{.State.Restarting}}"):
					// What a read of restarting alone would see now.
					return []byte(strings.Fields(second)[0]), nil
				case slices.ContainsFunc(c.Args, func(a string) bool { return strings.HasPrefix(a, "{{.State.Restarting}} ") }):
					return []byte(second), nil
				case slices.Contains(c.Args, "{{.State.Running}}"):
					return []byte("true"), nil
				}
				asked++
				if asked == 1 {
					return []byte("unhealthy"), nil
				}
				return []byte("healthy"), nil
			}}}
			if _, err := waitStackHealthy(context.Background(), dk, svcs, "s"); err != nil || asked != 2 {
				t.Fatalf("a need Docker was restarting, then healthy (asked %d times): %v", asked, err)
			}
		})
	}
}

// A reused need that stopped is started again where it is: the same
// container and volume, nothing removed or made again. One that runs is
// left alone. (21 Oct: greeting-api's exited postgres was judged unhealthy
// without ever being started.)
func TestAReusedNeedThatStoppedIsStartedAgain(t *testing.T) {
	t.Parallel()
	own := Ownership{Needs: map[string]string{"postgres": "/var/lib/postgresql/data"}}
	svcs := []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:16-alpine"}}
	for _, stopped := range []bool{true, false} {
		d := &standingDaemon{stopped: stopped}
		f := &fakeClient{reply: d.reply}
		if _, err := EnsureStandingServices(t.Context(), Scope{Client: f}, "env-shop-main", svcs, "production", own); err != nil {
			t.Fatalf("stopped=%v: %v", stopped, err)
		}
		if got := standingCalls(f, "start"); stopped != (len(got) == 1 && got[0] == "start apptown-svc-env-shop-main-postgres") || (!stopped && len(got) > 0) {
			t.Fatalf("stopped=%v: starts %v", stopped, got)
		}
		if len(standingCalls(f, "rm"))+len(standingCalls(f, "run"))+len(standingCalls(f, "volume")) > 0 {
			t.Fatalf("stopped=%v: the need was made again: %v", stopped, f.args)
		}
	}
}
