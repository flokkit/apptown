//go:build integration

package docker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
)

// testdata/sealed-app is an app behind a host allow-list built from
// FORGE_ORIGINS, as stock guidance has Rails and Django build theirs: its
// readiness path answers anyone, its pages answer only a host it was told,
// and /data answers 200 only when its database holds orders, printing a
// customer's address in the body. It is busybox nc in the Postgres image,
// so it reads its database with psql. orders.sql is the copy it starts on.

type sealedNeeds struct{}

func (sealedNeeds) Fulfil(ctx context.Context, needs []string, _ map[string]map[string]string) ([]deploy.ServiceSpec, map[string]string, error) {
	return []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:16-alpine", Health: "pg_isready -U app -d app",
			Env: map[string]string{"POSTGRES_DB": "app", "POSTGRES_USER": "app", "POSTGRES_PASSWORD": "app"}}},
		map[string]string{"PGHOST": "postgres", "PGUSER": "app", "PGPASSWORD": "app", "PGDATABASE": "app"}, nil
}
func (sealedNeeds) Data(context.Context, string) (string, error) { return "", nil }

type sealedSecrets struct{}

func (sealedSecrets) Resolve(context.Context, string, string, string, *appcontract.Contract) (Delivery, error) {
	return Delivery{}, nil
}

type sealedOrigin struct{ host string }

func (r sealedOrigin) FrontDoorHeaders() map[string]string {
	return map[string]string{"X-Forwarded-Proto": "https"}
}
func (r sealedOrigin) PublicOrigins(ctx context.Context) string { return "https://" + r.host }

// On a real daemon, a sealed boot (a restore rehearsal) fills its new
// Postgres before the app starts, gives the app the deployment's address as
// its only origin, and answers its probe from inside its own network with a
// status alone. It publishes no port: a container on another bridge cannot
// reach it. The boundary is the host, and the test records it: a container
// on the host's network can (design/subsystems/deployment.md, "Restore
// rehearsals").
//
// Kills: publishing the app's port; trusting the forge's tiers instead of
// the deployment's address (the page and the data check then answer 403);
// filling after the app starts; a probe that returns the body.
func TestIntegrationASealedBootHasNoAddressAndAnswersOnlyStatuses(t *testing.T) {
	dk := Scope{Client: CLI{Binary: "docker"}}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	stack, name, image := "rehearsal-it"+suffix, "forge-rehearsal-it"+suffix, "forge-sealed-app:it"+suffix
	cleanup := func() {
		_ = dk.RemoveDisposable(context.Background(), name)
		_ = dk.RemoveDisposableStack(context.Background(), stack)
		_ = exec.Command("docker", "rmi", "-f", image).Run()
	}
	cleanup()
	t.Cleanup(cleanup)

	build := filepath.Join("testdata", "sealed-app")
	if out, err := exec.Command("docker", "build", "-q", "-t", image, build).CombinedOutput(); err != nil {
		t.Fatalf("building the app: %v %s", err, out)
	}
	id := dk.Text(context.Background(), "image", "inspect", "-f", "{{.Id}}", image)

	c, err := appcontract.Parse([]byte("app:\n  web:\n    image: " + image + "\n    ports: [\"8080\"]\n    public: true\n    health: { readiness: { http: /healthz } }\nneeds:\n  postgres: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	const origin = "orders.example.com" // outside every domain this forge or the host has
	var appRunningAtFill bool
	sealed := &Sealed{ProbeImage: "alpine/git:v2.47.2", Host: origin}
	sealed.Fill = func(network string) error {
		appRunningAtFill = dk.Running(context.Background(), name)
		seed, err := os.Open(filepath.Join(build, "orders.sql"))
		if err != nil {
			return err
		}
		defer seed.Close()
		cmd := exec.Command("docker", "run", "--rm", "-i", "--network", network, "-e", "PGPASSWORD=app", "postgres:16-alpine",
			"psql", "-h", "postgres", "-U", "app", "-d", "app", "-v", "ON_ERROR_STOP=1")
		cmd.Stdin = seed
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v %s", err, out)
		}
		return nil
	}
	host := NewHost(Dependencies{Secrets: sealedSecrets{}, Needs: sealedNeeds{}, Routes: sealedOrigin{origin}})
	res, err := host.Boot(context.Background(), BootRequest{Name: name, StackID: stack, Workspace: t.TempDir(), Slug: "orders", Deployment: "prod", Docker: dk,
		Probe: 90 * time.Second, Sealed: sealed, Recorded: &RecordedImages{Parts: map[string]string{"web": id}}}, c)
	if err != nil {
		t.Fatalf("sealed boot: %v (%s)", err, AsBootError(err).LogTail)
	}
	if appRunningAtFill {
		t.Fatal("the app was running before its database was filled")
	}
	ctx := context.Background()
	for path, want := range map[string]int{"/healthz": 200, "/": 200, "/data": 200} {
		if got, err := sealed.Get(ctx, dk, res.Network, name, 8080, path); err != nil || got != want {
			t.Errorf("GET %s: %d %v, want %d", path, got, err, want)
		}
	}
	// The allow-list is real: a host the app was not told is refused.
	other := &Sealed{ProbeImage: sealed.ProbeImage, Host: "elsewhere.example"}
	if got, _ := other.Get(ctx, dk, res.Network, name, 8080, "/data"); got != 403 {
		t.Errorf("an untold host got %d, want 403", got)
	}

	// No address outside the host.
	if ports := dk.Text(ctx, "inspect", "-f", "{{json .HostConfig.PortBindings}}", name); ports != "{}" && ports != "null" {
		t.Fatalf("the app publishes ports: %s", ports)
	}
	if out, err := exec.Command("docker", "port", name).CombinedOutput(); strings.TrimSpace(string(out)) != "" {
		t.Fatalf("docker port answers %q (%v)", out, err)
	}
	ip := dk.Text(ctx, "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", name)
	if ip == "" {
		t.Fatal("the app has no address on its network")
	}
	reach := func(network string) bool {
		probe := fmt.Sprintf(`printf 'GET /healthz HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n' | nc -w 3 %s 8080 | head -n1 | grep -q 200`, ip)
		return exec.Command("docker", "run", "--rm", "--network", network, "--entrypoint", "", "alpine/git:v2.47.2", "sh", "-c", probe).Run() == nil
	}
	if reach("bridge") {
		t.Fatal("a container on another bridge reached the rehearsal")
	}
	// The boundary is the host, not the container: this is recorded, not
	// prevented. If it ever stops being true the deployment guide's words
	// should change with it.
	if reach("host") {
		t.Log("boundary: a container on the host's network reached the rehearsal, as the plan says")
	} else {
		t.Log("boundary: a host-network container did not reach the rehearsal on this daemon; the plan still treats the host as the boundary")
	}
}
