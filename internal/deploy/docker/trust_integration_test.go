//go:build integration

package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
	"github.com/flokkit/apptown/stock"
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
	// makes it trust the repository. Python reads SSL_CERT_FILE. Both
	// images are ones the tests pull anyway: the stock S3 client's Python,
	// and the PHP starter's Debian 12.
	const debian12 = "php:8.3.35-apache-bookworm@sha256:b79a53c3fb1dd4b9f350a8d99d84951512b6c21044e61bbecfee8af030b3722e"
	dockerfile := "FROM python:3.13.16-alpine3.24 AS fetch\n" +
		secret + `python3 -c 'import urllib.request; urllib.request.urlopen("` + server + `")' && touch /fetched` + "\n" +
		"FROM " + debian12 + "\nCOPY --from=fetch /fetched /fetched\n" +
		secret + `rm -f /etc/apt/sources.list.d/* /etc/apt/sources.list && echo "deb [trusted=yes] ` + apt + ` test main" > /etc/apt/sources.list.d/test.list && ` +
		// apt only warns about a source it could not fetch: the index's
		// fetch is what says it trusted the server.
		`apt-get update 2>&1 | tee /dev/stderr | grep -q '^Get:.* test Release'` + "\n"

	if _, out, err := build(dockerfile); err == nil {
		t.Fatalf("the build reached the company's servers with nothing configured:\n%s", out)
	}
	if _, out, err := build("FROM " + debian12 + "\n" + secret + "true\n"); err != nil {
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

// The go starter's shipped Dockerfile, built through the real build
// (Scope.Build), gives its Go app the extra certificates although the
// distroless runtime has no shell to run the installer: its build writes
// them into /etc/ssl/certs, beside the image's own roots, which stay the
// runtime's. With nothing configured the image holds only its own roots.
// The same source rebuilt on the same builder, with its cache, after the
// certificates change or are removed holds the current ones, not the cached.
// Not parallel: it installs the process's trust.
func TestIntegrationTheGoStartersImageTrustsTheExtraCertificates(t *testing.T) {
	a, b := trusttest.New(t, "Company Inspecting CA A"), trusttest.New(t, "Company Inspecting CA B")
	servers := map[*trusttest.Authority]string{
		a: fmt.Sprintf("https://host.docker.internal:%d/", a.Serve(t, trusttest.OK)),
		b: fmt.Sprintf("https://host.docker.internal:%d/", b.Serve(t, trusttest.OK)),
	}
	dockerfile, err := fs.ReadFile(stock.FS, "seeds/go/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dk := Scope{Client: CLI{Binary: "docker"}}
	dir := t.TempDir()
	for name, body := range map[string]string{
		"Dockerfile": string(dockerfile),
		"go.mod":     "module probe\n\ngo 1.22\n",
		// The app: one HTTPS request to its argument.
		"main.go": "package main\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n\t\"os\"\n)\n\n" +
			"func main() {\n\tr, err := http.Get(os.Args[1])\n\tif err != nil {\n\t\tfmt.Println(err)\n\t\tos.Exit(1)\n\t}\n\tfmt.Println(r.Status)\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// certs is an image's /etc/ssl/certs, file by file.
	certs := func(img string) map[string]string {
		out, err := dk.Run(ctx, "create", "-q", img, "/app")
		if err != nil {
			t.Fatalf("creating a container of %s: %v\n%s", img, err, out)
		}
		id := strings.TrimSpace(string(out))
		defer func() { _, _ = dk.Run(context.Background(), "rm", "-f", id) }()
		var tarball bytes.Buffer
		if out, err := dk.Command(ctx, Command{Args: []string{"cp", id + ":/etc/ssl/certs", "-"}, Stdout: &tarball}); err != nil {
			t.Fatalf("reading %s's certificates: %v\n%s", img, err, out)
		}
		files := map[string]string{}
		r := tar.NewReader(&tarball)
		for {
			h, err := r.Next()
			if err == io.EOF {
				return files
			}
			if err != nil {
				t.Fatal(err)
			}
			if h.Typeflag == tar.TypeReg {
				body, _ := io.ReadAll(r)
				files[path.Base(h.Name)] = string(body)
			}
		}
	}
	base := certs("gcr.io/distroless/static-debian12:nonroot")
	if len(base) != 1 || base["ca-certificates.crt"] == "" {
		t.Fatalf("the runtime image's own certificates are not one ca-certificates.crt: %v", keys(base))
	}

	// Each build of the same source with these extras (none, A, B, none)
	// trusts exactly them, beside the runtime's own roots.
	for i, extras := range []*trusttest.Authority{nil, a, b, nil} {
		trust.Install(nil)
		if extras != nil {
			trusttest.Install(t, extras.Bundle(t), trust.Bundle{})
		}
		tag := "apptown-trust-go:" + strings.ToLower(ids.New(time.Now()).String())
		t.Cleanup(func() { _, _ = dk.Run(context.Background(), "rmi", "-f", tag) })
		if out, err := dk.Build(ctx, "-q", "-t", tag, dir); err != nil {
			t.Fatalf("build %d: the go starter's Dockerfile: %v\n%s", i, err, out)
		}
		for authority, server := range servers {
			out, err := dk.Run(ctx, "run", "--rm", "--add-host", "host.docker.internal:host-gateway", tag, "/app", server)
			reached := err == nil && strings.TrimSpace(string(out)) == "200 OK"
			if reached != (authority == extras) {
				t.Fatalf("build %d: the app reached %s: %v, want %v\n%s", i, server, reached, authority == extras, out)
			}
		}
		got := certs(tag)
		want := map[string]string{"ca-certificates.crt": base["ca-certificates.crt"]}
		if extras != nil {
			want["apptown-trust.pem"] = strings.TrimSpace(string(extras.PEM()))
			got["apptown-trust.pem"] = strings.TrimSpace(got["apptown-trust.pem"])
		}
		if !maps.Equal(got, want) {
			t.Fatalf("build %d: the image's certificates are %v, not its own roots and the extras beside them", i, keys(got))
		}
	}
}

func keys(m map[string]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
