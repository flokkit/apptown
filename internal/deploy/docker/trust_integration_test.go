//go:build integration

package docker

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// emptyPackages is the SHA-256 of an empty file: an apt index with no
// package in it.
const emptyPackages = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// aptRepository is an HTTPS apt repository with no packages (suite test,
// component main), signed by nobody: a source names it [trusted=yes].
func aptRepository() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/dists/test/Release":
			fmt.Fprintf(w, "Origin: test\nLabel: test\nSuite: test\nCodename: test\nDate: Thu, 23 Oct 2026 00:00:00 UTC\nArchitectures: amd64 arm64\nComponents: main\nSHA256:\n %s 0 main/binary-amd64/Packages\n %s 0 main/binary-arm64/Packages\n", emptyPackages, emptyPackages)
		case strings.HasSuffix(r.URL.Path, "/Packages"):
		default:
			http.NotFound(w, r)
		}
	})
}

// A Dockerfile's RUN line that mounts the build secret and runs it reaches
// an HTTPS server and an HTTPS apt repository signed by the extra authority,
// through the real build (Scope.Build). Nothing of it stays in the image:
// no temporary folder, no apt setting, the system store as it was. With
// nothing configured the same secret mount is no required input.
// Not parallel: it installs the process's trust.
func TestIntegrationABuildTrustsTheExtraCertificatesThroughItsSecret(t *testing.T) {
	company := trusttest.New(t, "Company Inspecting CA")
	server := fmt.Sprintf("https://host.docker.internal:%d/", company.Serve(t, trusttest.OK))
	apt := fmt.Sprintf("https://host.docker.internal:%d/", company.Serve(t, aptRepository()))
	dk := Scope{Client: CLI{Binary: "docker"}}
	build := func(dockerfile string) (string, []byte, error) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
			t.Fatal(err)
		}
		tag := "apptown-trust-build:" + strings.ToLower(ids.New(time.Now()).String())
		t.Cleanup(func() { _, _ = dk.Run(context.Background(), "rmi", "-f", tag) })
		out, err := dk.Build(context.Background(), "--no-cache", "--add-host", "host.docker.internal:host-gateway", "-t", tag, dir)
		return tag, out, err
	}
	const secret = `RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; `
	// Debian 12's apt reads only its own setting or the system store
	// (GnuTLS), not the variables: the installer's apt setting is what
	// makes it trust the repository. Python reads SSL_CERT_FILE.
	dockerfile := "FROM python:3.13-slim-trixie AS fetch\n" +
		secret + `python3 -c 'import urllib.request; urllib.request.urlopen("` + server + `")' && touch /fetched` + "\n" +
		"FROM debian:bookworm-slim\nCOPY --from=fetch /fetched /fetched\n" +
		secret + `rm -f /etc/apt/sources.list.d/* /etc/apt/sources.list && echo "deb [trusted=yes] ` + apt + ` test main" > /etc/apt/sources.list.d/test.list && ` +
		// apt only warns about a source it could not fetch: the index's
		// fetch is what says it trusted the server.
		`apt-get update 2>&1 | tee /dev/stderr | grep -q '^Get:.* test Release'` + "\n"

	if _, out, err := build(dockerfile); err == nil {
		t.Fatalf("the build reached the company's servers with nothing configured:\n%s", out)
	}
	if _, out, err := build("FROM debian:bookworm-slim\n" + secret + "true\n"); err != nil {
		t.Fatalf("the secret's mount is a required input with nothing configured: %v\n%s", err, out)
	}

	trusttest.Install(t, company.Bundle(t), trust.Bundle{})
	tag, out, err := build(dockerfile)
	if err != nil {
		t.Fatalf("the build with the extra certificates: %v\n%s", err, out)
	}
	pemLine := strings.Split(strings.TrimSpace(string(company.PEM())), "\n")[1]
	left, err := dk.Run(context.Background(), "run", "--rm", "--entrypoint", "sh", tag, "-c",
		`ls -A /tmp; env | grep -E 'APT_CONFIG|SSL_CERT_FILE|APPTOWN' ; cat /etc/ssl/certs/ca-certificates.crt 2>/dev/null | grep -c '`+pemLine+`'; true`)
	if err != nil || strings.TrimSpace(string(left)) != "0" {
		t.Fatalf("the build left something of the certificates in the image: %v\n%s", err, left)
	}
}

// An app's container started the way AppTown starts it, whose command runs
// the installer first (as the starters' CMD does), reaches a server signed
// by the extra authority.
// Not parallel: it installs the process's trust.
func TestIntegrationAnAppsContainerTrustsTheExtraCertificates(t *testing.T) {
	company := trusttest.New(t, "Company Inspecting CA")
	server := fmt.Sprintf("https://host.docker.internal:%d/", company.Serve(t, trusttest.OK))
	trusttest.Install(t, company.Bundle(t), trust.Bundle{})
	dk := Scope{Client: CLI{Binary: "docker"}}
	name := "apptown-trust-app-" + strings.ToLower(ids.New(time.Now()).String())
	t.Cleanup(func() { _, _ = dk.Run(context.Background(), "rm", "-f", name) })
	args := append([]string{"run", "--name", name, "--add-host", "host.docker.internal:host-gateway"}, trust.ContainerArgs(true)...)
	args = append(args, "curlimages/curl:8.22.0", "sh", "-c", `eval "${APPTOWN_TRUST:-}"; exec "$@"`, "sh", "curl", "-fsS", server)
	if out, err := dk.Run(context.Background(), args...); err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("%v: %s", err, out)
	}
}
