//go:build integration

package trust_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// The installer in real images, against two HTTPS servers: one the image
// itself trusts (its own authority, in its own store) and one signed by the
// extra authority AppTown gives it. After the script, both answer, for each
// way programs read roots; before it, AppTown's does not.

// image builds a small image from dockerfile, with the image's own
// authority beside it as image-ca.pem.
func image(t *testing.T, own *trusttest.Authority, dockerfile string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["Dockerfile"] = dockerfile
	files["image-ca.pem"] = string(own.PEM())
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tag := "apptown-trust-test:" + strings.ToLower(ids.New(time.Now()).String())
	dk := docker.Scope{Client: docker.CLI{}}
	if out, err := dk.Build(context.Background(), "-q", "-t", tag, dir); err != nil {
		t.Fatalf("building %s: %v: %s", tag, err, out)
	}
	t.Cleanup(func() { _, _ = dk.Run(context.Background(), "rmi", "-f", tag) })
	return tag
}

// run runs script in img as AppTown runs a container: given the script by
// name (execution's dockerArgs), reaching this machine at
// host.docker.internal.
func run(t *testing.T, img, script string) execution.Outcome {
	t.Helper()
	return execution.Runner{}.Run(context.Background(), execution.Docker, execution.Request{
		Image: img, Argv: []string{"sh", "-c", script}, NoEntrypoint: true,
		ExtraHosts: []string{"host.docker.internal:host-gateway"}, Timeout: 3 * time.Minute,
		Name: execution.UniqueName("apptown-trust-test"),
	})
}

func ok(t *testing.T, what string, o execution.Outcome) {
	t.Helper()
	if o.RunErr != nil || o.ExitCode != 0 {
		t.Fatalf("%s: exit %d %v\n%s", what, o.ExitCode, o.RunErr, o.Output)
	}
}

func fails(t *testing.T, what string, o execution.Outcome) {
	t.Helper()
	if o.RunErr == nil && o.ExitCode == 0 {
		t.Fatalf("%s succeeded without the extra certificates:\n%s", what, o.Output)
	}
}

// Not parallel: it installs the process's trust.
func TestIntegrationTheInstallerAddsToTheImagesOwnRoots(t *testing.T) {
	own, company := trusttest.New(t, "Image's Own CA"), trusttest.New(t, "Company Inspecting CA")
	ownURL := fmt.Sprintf("https://host.docker.internal:%d/", own.Serve(t, trusttest.OK))
	companyURL := fmt.Sprintf("https://host.docker.internal:%d/", company.Serve(t, trusttest.OK))

	// The system store, for curl-less Python (SSL_CERT_FILE), as root and
	// as an unprivileged user.
	py := image(t, own, "FROM python:3.13.16-alpine3.24\nCOPY image-ca.pem /image-ca.pem\n"+
		"RUN cat /image-ca.pem >> /etc/ssl/certs/ca-certificates.crt && adduser -D -u 10001 app\n", map[string]string{})
	get := `python3 -c 'import sys, urllib.request; [urllib.request.urlopen(u) for u in sys.argv[1:]]' ` + ownURL + " " + companyURL
	// curl reading the file the image itself names (CURL_CA_BUNDLE), which
	// holds only the image's authority.
	curl := image(t, own, "FROM curlimages/curl:8.22.0\nCOPY image-ca.pem /own.pem\nENV CURL_CA_BUNDLE=/own.pem\n", map[string]string{})
	curlBoth := "curl -fsS -o /dev/null " + ownURL + " && curl -fsS -o /dev/null " + companyURL
	// Java: the image's own authority in jssecacerts, which the JVM
	// prefers to cacerts.
	java, err := os.ReadFile("testdata/Get.java")
	if err != nil {
		t.Fatal(err)
	}
	jvm := image(t, own, "FROM eclipse-temurin:21.0.12.1_1-jdk\nCOPY image-ca.pem Get.java /\n"+
		"RUN cp \"$JAVA_HOME/lib/security/cacerts\" \"$JAVA_HOME/lib/security/jssecacerts\" && "+
		"keytool -importcert -noprompt -keystore \"$JAVA_HOME/lib/security/jssecacerts\" -storepass changeit -alias image -file /image-ca.pem && "+
		"sha256sum \"$JAVA_HOME/lib/security/jssecacerts\" > /jssecacerts.sum\n", map[string]string{"Get.java": string(java)})
	javaBoth := "java /Get.java " + ownURL + " " + companyURL + ` && sha256sum -c /jssecacerts.sum`

	// Without the script AppTown's server is not trusted.
	fails(t, "python", run(t, py, get))
	fails(t, "curl", run(t, curl, curlBoth))
	fails(t, "java", run(t, jvm, javaBoth))
	fails(t, "node", run(t, "node:24-slim", `node -e 'fetch(process.argv[1]).then(r => process.exit(r.ok ? 0 : 1), () => process.exit(1))' `+companyURL))

	trusttest.Install(t, company.Bundle(t), trust.Bundle{})
	ok(t, "python as root", run(t, py, trust.Run+"; "+get))
	ok(t, "python as a user", run(t, py, `su app -s /bin/sh -c '`+strings.ReplaceAll(trust.Run+"; "+get, "'", `'\''`)+`'`))
	ok(t, "curl with the image's own bundle", run(t, curl, trust.Run+"; "+curlBoth))
	ok(t, "java with the image's own jssecacerts", run(t, jvm, trust.Run+"; "+javaBoth))
	ok(t, "node", run(t, "node:24-slim", trust.Run+`; node -e 'fetch(process.argv[1]).then(r => process.exit(r.ok ? 0 : 1), e => { console.error(e); process.exit(1) })' `+companyURL))
}
