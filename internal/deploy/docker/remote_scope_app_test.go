package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

type remoteAppClient struct {
	calls []struct{ host, command string }
}

func (c *remoteAppClient) Run(_ context.Context, host string, req Command) ([]byte, error) {
	c.calls = append(c.calls, struct{ host, command string }{host, strings.Join(req.Args, " ")})
	if len(req.Args) == 0 {
		return nil, nil
	}
	switch req.Args[0] {
	case "port":
		return []byte("0.0.0.0:40001\n"), nil
	case "image":
		return []byte("\n"), nil
	}
	return nil, nil
}

// An attached host receives every build, container, network and port command.
func TestRemoteScopeBootsOnTheAttachedHost(t *testing.T) {
	t.Parallel()
	client := &remoteAppClient{}
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "Dockerfile"), []byte("FROM alpine:3.20\nUSER 10001\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dk := (Scope{Client: client}).WithHost("ssh://apptown-target-droplet", "droplet.example", "apps.example.com")
	c := &appcontract.Part{Build: ".", Port: 8080, DataDir: "/data"}
	port, err := NewHost(Dependencies{}).StartAppContainer(t.Context(), BootRequest{Docker: dk, Name: "apptown-env-waypoint-main", Slug: "waypoint", Deployment: "main", Workspace: ws, Persistent: true}, c, "", nil)
	if err != nil || port != 40001 {
		t.Fatalf("start on the attached host: port=%d err=%v calls=%v", port, err, client.calls)
	}
	var remote []string
	for _, call := range client.calls {
		if call.host != "ssh://apptown-target-droplet" {
			t.Fatalf("command escaped attached host: %+v", call)
		}
		remote = append(remote, call.host+" "+call.command)
	}
	joined := strings.Join(remote, "\n")
	for _, want := range []string{
		"ssh://apptown-target-droplet build -t apptown-preview-build-apptown-env-waypoint-main",
		"traefik.http.routers.apptown-env-waypoint-main.rule=Host(`waypoint.apps.example.com`)",
		"traefik.http.services.apptown-env-waypoint-main.loadbalancer.server.port=8080",
		"traefik.docker.network=traefik",
		"ssh://apptown-target-droplet network connect traefik apptown-env-waypoint-main",
		"ssh://apptown-target-droplet port apptown-env-waypoint-main 8080",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in remote commands:\n%s", want, joined)
		}
	}
	// Traefik reads a container when it starts: the container is created,
	// joins the traefik network, and only then starts.
	order := func(prefix string) int {
		for i, r := range remote {
			if strings.HasPrefix(r, "ssh://apptown-target-droplet "+prefix) {
				return i
			}
		}
		t.Fatalf("no %q in remote commands:\n%s", prefix, joined)
		return -1
	}
	if c, n, s := order("create "), order("network connect traefik"), order("start apptown-env-waypoint-main"); !(c < n && n < s) {
		t.Fatalf("create %d, connect %d, start %d: the container must join traefik before it starts:\n%s", c, n, s, joined)
	}
	if strings.Contains(joined, "ssh://apptown-target-droplet run -d") {
		t.Fatalf("a routed app was started by run before joining traefik:\n%s", joined)
	}
}

// AppTown's own daemon publishes an app's port on loopback only (the
// AppTown's proxy is the front door); an attached host publishes it where
// AppTown's probe and the plain front door, both at the host's address,
// reach it. A loopback bind there would leave the readiness probe
// unanswered.
func TestAnAppPortIsPublishedWhereItsProbeReachesIt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, host, want, probe string
	}{
		{"AppTown's own daemon", "", "-p 127.0.0.1::8080 ", "http://127.0.0.1:40001"},
		{"an attached host", "ssh://apptown-target-lab", "-p 0.0.0.0::8080 ", "http://192.0.2.10:40001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &remoteAppClient{}
			dk := (Scope{Client: client}).WithHost(tc.host, "192.0.2.10", "")
			port, err := NewHost(Dependencies{}).StartAppContainer(t.Context(), BootRequest{Docker: dk, Name: "apptown-env-site-main", Slug: "site", Deployment: "main", Workspace: t.TempDir(), Persistent: true},
				&appcontract.Part{Image: "example/site:1", Port: 8080}, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			var run string
			for _, call := range client.calls {
				if strings.HasPrefix(call.command, "run ") {
					run = call.command
				}
			}
			if !strings.Contains(run, tc.want) {
				t.Fatalf("published with %q, want %q", run, tc.want)
			}
			if got := dk.ProbeURL(port); got != tc.probe {
				t.Fatalf("probed at %s, want %s", got, tc.probe)
			}
		})
	}
}

// An app is told every origin a browser reaches it at: this AppTown's tiers
// and, when an attached host's Traefik routes it, that host's ingress
// domain. Given only the preview tier, a framework that checks the host
// (Rails) refuses every page at <slug>.apps.<domain>.
func TestARoutedAppTrustsItsAttachedHostsDomain(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, domain, want string
	}{
		{"routed by the attached host", "apps.example.com", "APPTOWN_ORIGINS=https://change-1.previews.example,https://*.apps.example.com "},
		{"an attached host with no ingress domain", "", "APPTOWN_ORIGINS=https://change-1.previews.example "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &remoteAppClient{}
			dk := (Scope{Client: client}).WithHost("ssh://apptown-target-droplet", "droplet.example", tc.domain)
			if _, err := NewHost(Dependencies{Routes: liveRoutes{}}).StartAppContainer(t.Context(), BootRequest{Docker: dk, Name: "apptown-env-waypoint-main", Slug: "waypoint", Deployment: "main", Workspace: t.TempDir(), Persistent: true},
				&appcontract.Part{Image: "example/site:1", Port: 8080}, "", nil); err != nil {
				t.Fatal(err)
			}
			var started string
			for _, call := range client.calls {
				if strings.HasPrefix(call.command, "run ") || strings.HasPrefix(call.command, "create ") {
					started = call.command
				}
			}
			if !strings.Contains(started, "-e "+tc.want) {
				t.Fatalf("container started without %q:\n%s", tc.want, started)
			}
		})
	}
}

// A container that will not start says why: the daemon's error is at the end
// of a long answer, so the failure keeps the end and counts what it dropped.
func TestAFailedStartKeepsTheEndOfTheDaemonsAnswer(t *testing.T) {
	t.Parallel()
	answer := strings.Repeat("pulling layer\n", 300) + "Error: port 8080 is already allocated"
	for _, c := range []struct {
		name, domain, fails string
	}{
		{"run", "", "run"},
		{"start", "apps.example.com", "start"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{reply: func(req Command) ([]byte, error) {
				if len(req.Args) > 0 && req.Args[0] == c.fails {
					return []byte(answer), errors.New("exit status 125")
				}
				if len(req.Args) > 0 && req.Args[0] == "image" {
					return []byte("\n"), nil
				}
				return nil, nil
			}}
			dk := (Scope{Client: client}).WithHost("ssh://h", "h.example", c.domain)
			_, err := NewHost(Dependencies{}).StartAppContainer(t.Context(), BootRequest{Docker: dk, Name: "apptown-env-shop-main", Slug: "shop", Deployment: "main", Workspace: t.TempDir()}, &appcontract.Part{Image: "shop:1", Port: 8080}, "", nil)
			if err == nil || !strings.Contains(err.Error(), "docker "+c.fails+": …(") || !strings.HasSuffix(err.Error(), "Error: port 8080 is already allocated") || len(err.Error()) > startFailureTail+100 {
				t.Fatalf("start failure (%d bytes): %v", len(fmt.Sprint(err)), err)
			}
		})
	}
}

// Two deployments of one project on an attached host with an ingress
// domain: the host's Traefik routes the project's name to main alone.
// Traefik joins containers that carry the same router into one service and
// splits requests between them, so a second deployment carrying main's
// router took about half of main's visitors (soak finding 55). Any other
// deployment carries no router and is reached at the host and its port.
func TestMainsTrafficNeverReachesAnotherDeployment(t *testing.T) {
	t.Parallel()
	started := map[string]string{}
	for _, deployment := range []string{"main", "fresh"} {
		client := &remoteAppClient{}
		dk := (Scope{Client: client}).WithHost("ssh://apptown-target-droplet", "droplet.example", "apps.example.com")
		name := ByKey(keyed(byte(len(started) + 1))).App()
		if _, err := NewHost(Dependencies{}).StartAppContainer(t.Context(), BootRequest{Docker: dk, Name: name, Slug: "bkproof", Deployment: deployment, Workspace: t.TempDir(), Persistent: true},
			&appcontract.Part{Image: "example/bkproof:1", Port: 8080}, "", nil); err != nil {
			t.Fatal(err)
		}
		for _, call := range client.calls {
			if strings.HasPrefix(call.command, "run ") || strings.HasPrefix(call.command, "create ") {
				started[deployment] = call.command
			}
		}
	}
	if !strings.Contains(started["main"], "traefik.http.routers."+ByKey(keyed(1)).App()+".rule=Host(`bkproof.apps.example.com`)") {
		t.Fatalf("main is not routed at the project's name:\n%s", started["main"])
	}
	if strings.Contains(started["fresh"], "traefik.") {
		t.Fatalf("another deployment carries a Traefik route, so it can take main's visitors:\n%s", started["fresh"])
	}
	// The addresses AppTown shows and records agree with the route.
	dk := (Scope{}).WithHost("ssh://apptown-target-droplet", "droplet.example", "apps.example.com")
	if got := dk.FrontDoor("bkproof", "main", 40001); got != "https://bkproof.apps.example.com" {
		t.Fatalf("main is shown at %s", got)
	}
	if got := dk.FrontDoor("bkproof", "fresh", 40002); got != "http://droplet.example:40002" {
		t.Fatalf("another deployment is shown at %s, want the host and its port", got)
	}
}
