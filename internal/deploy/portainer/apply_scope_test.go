package portainer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

func TestScopeChangeAfterPreflightPreventsApply(t *testing.T) {
	t.Parallel()
	compose, ready := readinessFixture(t)
	artifact, err := WithReadiness(compose, ready)
	if err != nil {
		t.Fatal(err)
	}
	var changed atomic.Bool
	var applies atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/stacks":
			fmt.Fprint(w, "[]")
		case r.Method == "POST" && r.URL.Path == "/api/stacks/create/standalone/string":
			applies.Add(1)
			fmt.Fprint(w, `{"Id":19}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(server.Close)
	scope := Scope{Version: 1, APIBase: server.URL, EndpointID: 3, DaemonID: "admitted-daemon"}
	driver := Driver{
		TargetID: ready.Target, ReadinessTimeout: time.Millisecond,
		Allowed: func(context.Context) error { return nil },
		Config: func(context.Context, ids.ID) (*Target, error) {
			if changed.Load() {
				return nil, errors.New("current target no longer verifies against admitted daemon")
			}
			return &Target{PortainerURL: server.URL, Scope: &scope}, nil
		},
	}
	driver.Diagnostics = func(context.Context, Target) (Diagnostic, func() error, error) {
		daemon := containerresulttest.Daemon{Answers: func(containerresulttest.Command) (containerresulttest.Answer, bool) {
			return containerresulttest.Answer{Answer: []byte("apptown-readiness-v1")}, true
		}}
		return Diagnostic{Command: func(ctx context.Context, env []string, args ...string) ([]byte, error) {
			out, err, ok := daemon.RunEnv(args, env)
			if !ok {
				return nil, fmt.Errorf("unexpected command %v", args)
			}
			// The final successful preflight effect completes; current verification
			// subsequently refuses the destination while release permission remains.
			if len(args) > 0 && args[0] == "rm" {
				changed.Store(true)
			}
			return out, err
		}}, func() error { return nil }, nil
	}
	_, err = driver.Reconcile(t.Context(), deploy.Placement{ID: ready.Deployment}, 1,
		deploy.Artifact{Kind: "compose", Body: []byte(artifact), Hash: deploy.ConfigHash(strings.TrimSpace(artifact))}, "request", func(string, ...any) {}, func(map[string]any) error { return nil })
	if !changed.Load() {
		t.Fatal("preflight did not finish")
	}
	if applies.Load() != 0 {
		t.Fatalf("applied %d stack(s) after current provider scope became unverifiable; result: %v", applies.Load(), err)
	}
	if err == nil {
		t.Fatal("expected refusal")
	}
}
