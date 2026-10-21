package deployment

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/ids/idstest"
)

// A standing deployment other than main answers at the port AppTown
// recorded for it however often Docker starts its container again: a
// restart (the health controller's), and a stop and start (a restore's).
// AppTown probes and shows that recorded port, so a port Docker picks
// afresh at each start leaves the probe asking a dead port and the health
// controller restarting the app again and again (soak finding 58,
// 19 October 2026).
func TestAReleasedDeploymentKeepsItsPortWhenDockerStartsItAgain(t *testing.T) {
	t.Parallel()
	w := noLocalDockerWorld(t, multipartApp)
	w.daemon.PublishPorts()
	t.Cleanup(w.daemon.Close)
	s := w.service(true)
	main := w.deployment(t, s, "main", w.remote)
	w.deployment(t, s, "staging", w.remote)
	_, bundle := w.land(t, s, main, idstest.N(7))
	if _, err := w.release(t, s, bundle.ID, "staging"); err != nil {
		t.Fatal(err)
	}
	container := docker.Current.Container("game", "staging")
	recorded := func() int {
		dep, err := s.Deployments.ByName(context.Background(), w.project, "staging")
		if err != nil || dep.HostPort == 0 {
			t.Fatalf("staging's recorded port: %+v %v", dep, err)
		}
		return dep.HostPort
	}
	port := recorded()
	if runs := strings.Join(w.daemon.verbs("run"), "\n"); !strings.Contains(runs, fmt.Sprintf("--name %s ", container)) || !strings.Contains(runs, fmt.Sprintf(" -p 0.0.0.0:%d:8080 ", port)) {
		t.Fatalf("staging's first boot is not published at its recorded port %d:\n%s", port, runs)
	}
	client := http.Client{Timeout: 2 * time.Second}
	for _, again := range [][]string{{"restart", container}, {"stop", container}, {"start", container}} {
		if _, err := w.daemon.Run(context.Background(), attachedHost, docker.Command{Args: again}); err != nil {
			t.Fatalf("docker %v: %v", again, err)
		}
		if again[0] == "stop" {
			continue
		}
		if got := recorded(); got != port {
			t.Fatalf("after docker %s the recorded port moved: %d, then %d", again[0], port, got)
		}
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err != nil {
			t.Fatalf("after docker %s staging no longer answers at its recorded port %d: %v", again[0], port, err)
		}
		resp.Body.Close()
	}
}

// A release whose recorded port something else holds fails with that
// reason, and the deployment is given a fresh port for its next boot, as a
// landing is (Deployments.ForgetTakenPort); otherwise every release would
// fail the same way.
func TestAReleaseWhosePortIsTakenGetsAFreshOneNextTime(t *testing.T) {
	t.Parallel()
	w := noLocalDockerWorld(t, multipartApp)
	w.daemon.PublishPorts()
	t.Cleanup(w.daemon.Close)
	s := w.service(true)
	main := w.deployment(t, s, "main", w.remote)
	w.deployment(t, s, "staging", w.remote)
	_, bundle := w.land(t, s, main, idstest.N(7))
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	taken := held.Addr().(*net.TCPAddr).Port
	if _, err := s.Deployments.ReserveHostPort(t.Context(), w.project, "staging", docker.Current.Container("game", "staging"), taken); err != nil {
		t.Fatal(err)
	}
	if _, err := w.release(t, s, bundle.ID, "staging"); err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("a release at a taken port: %v", err)
	}
	dep, err := s.Deployments.ByName(t.Context(), w.project, "staging")
	if err != nil || dep.HostPort == 0 || dep.HostPort == taken {
		t.Fatalf("after the bind failure staging's port is still %d (taken %d): %v", dep.HostPort, taken, err)
	}
}
