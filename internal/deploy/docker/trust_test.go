package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// The Docker client gives each command the extra certificates for where it
// runs: this machine's Docker gets the local authority too, an attached
// host (--host) only the company's.
// Not parallel: it installs the process's trust.
func TestTheDockerClientGivesEachDaemonItsOwnScript(t *testing.T) {
	company, local := trusttest.New(t, "Company CA"), trusttest.New(t, "Local CA")
	trusttest.Install(t, company.Bundle(t), local.Bundle(t))
	bin := echoingDocker(t, trust.EnvVar)
	has := func(script string, a *trusttest.Authority) bool {
		return strings.Contains(script, strings.TrimSpace(string(a.PEM())))
	}
	here, err := Scope{Client: CLI{Binary: bin}}.Run(context.Background(), "run", "img")
	if err != nil || !has(string(here), company) || !has(string(here), local) {
		t.Fatalf("this machine's daemon: %v\n%s", err, here)
	}
	there, err := Scope{Client: CLI{Binary: bin}}.WithHost("tcp://attached:2376", "attached", "apps.example").Run(context.Background(), "run", "img")
	if err != nil || !has(string(there), company) || has(string(there), local) {
		t.Fatalf("an attached host's daemon: %v\n%s", err, there)
	}
}

// An app's containers (its public part, the other parts, its init steps)
// and its builds are given the extra certificates; a static part's file
// server and a need's service are not, and nothing is given on an attached
// host when the only certificate is this machine's local authority.
// Not parallel: it installs the process's trust.
func TestAnAppsContainersAndBuildsAreGivenTheExtraCertificates(t *testing.T) {
	trusttest.Install(t, trusttest.New(t, "Company CA").Bundle(t), trusttest.New(t, "Local CA").Bundle(t))
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "Dockerfile"), []byte("FROM alpine:3.20\nUSER 10001\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	start := func(dk Scope) string {
		client := dk.Client.(*remoteAppClient)
		if _, err := NewHost(Dependencies{StaticServer: testStaticServer, DFImage: testDF}).startAppContainer(t.Context(), BootRequest{Allowed: AuthorisedWhenRequested, Docker: dk, Name: "apptown-env-w-main", Slug: "w", ServingLabel: "w", Deployment: "main", Workspace: ws}, &appcontract.Part{Build: ".", Port: 8080}, "", nil); err != nil {
			t.Fatal(err)
		}
		plan := &appcontract.Plan{Init: []appcontract.InitContainer{{Name: "migrate", Image: "app:1"}}}
		if err := runInitContainers(t.Context(), dk, "id", "net", plan, Delivery{Env: func(string) map[string]string { return nil }}); err != nil {
			t.Fatal(err)
		}
		if err := runServiceContainersWith(t.Context(), dk, "id", "net", []deploy.ServiceSpec{{Name: "worker", Image: "app:1", AppPart: true}, {Name: "db", Image: "postgres:16"}}, serviceRunOptions{class: "service"}, Ownership{}); err != nil {
			t.Fatal(err)
		}
		var lines []string
		for _, c := range client.calls {
			lines = append(lines, c.command)
		}
		return strings.Join(lines, "\n")
	}
	calls := start(Scope{Client: &remoteAppClient{}})
	for _, want := range []string{
		"build --secret id=apptown-trust,env=APPTOWN_TRUST ",
		"-e APPTOWN_TRUST apptown-preview-build-apptown-env-w-main", // the public part
		"-e APPTOWN_TRUST app:1",                                    // the init step
	} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %q in:\n%s", want, calls)
		}
	}
	for _, line := range strings.Split(calls, "\n") {
		worker, db := strings.Contains(line, "--network-alias worker"), strings.Contains(line, "--network-alias db")
		if worker && !strings.Contains(line, "-e APPTOWN_TRUST") || db && strings.Contains(line, "APPTOWN_TRUST") {
			t.Fatalf("a part must be given the certificates and a need not: %s", line)
		}
	}
	if calls := start((Scope{Client: &remoteAppClient{}}).WithHost("ssh://attached", "attached", "apps.example")); !strings.Contains(calls, "APPTOWN_TRUST") {
		t.Fatalf("the attached host is not given the company's certificate:\n%s", calls)
	}
	trusttest.Install(t, trust.Bundle{}, trusttest.New(t, "Local CA").Bundle(t))
	if calls := start((Scope{Client: &remoteAppClient{}}).WithHost("ssh://attached", "attached", "apps.example")); strings.Contains(calls, "APPTOWN_TRUST") {
		t.Fatalf("an attached host was given this machine's local authority:\n%s", calls)
	}
}
