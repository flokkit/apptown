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

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/poll"
)

// The static site's image, built from StaticSiteDockerfile and run with no
// command, as App Platform and every other target runs it: it serves the
// site, /_forge/env.js with the values it was started with, and the
// release identity it was told at ReleasePath. (An image without its own
// command would serve the file server's default page on App Platform.)
func TestIntegrationAStaticSiteImageServesItselfWithNoCommand(t *testing.T) {
	site := t.TempDir()
	page := `<html><script src="/_forge/env.js"></script></html>`
	if err := os.WriteFile(filepath.Join(site, "index.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	dockerfile := filepath.Join(t.TempDir(), "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte(StaticSiteDockerfile(StaticServerImage, 8080)), 0o644); err != nil {
		t.Fatal(err)
	}
	tag := fmt.Sprintf("forge-static-self-test:%d", time.Now().UnixNano())
	if out, err := exec.Command("docker", "build", "-q", "-f", dockerfile, "-t", tag, site).CombinedOutput(); err != nil {
		t.Fatalf("docker build: %s", out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })
	start := func(env ...string) string {
		t.Helper()
		args := append([]string{"run", "-d", "-p", "127.0.0.1::8080"}, env...)
		out, err := exec.Command("docker", append(args, tag)...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker run: %s", out)
		}
		id := strings.TrimSpace(string(out))
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
		port, err := exec.Command("docker", "port", id, "8080/tcp").Output()
		if err != nil {
			t.Fatalf("docker port: %v", err)
		}
		base := "http://" + strings.TrimSpace(strings.Split(string(port), "\n")[0])
		if !poll.Until(context.Background(), 30*time.Second, 100*time.Millisecond, func() bool {
			code, _, err := check.HTTPFetcher.Fetch(context.Background(), base+"/")
			return err == nil && code == 200
		}) {
			logs, _ := exec.Command("docker", "logs", id).CombinedOutput()
			t.Fatalf("the image did not serve: %s", logs)
		}
		return base
	}
	base := start("-e", appcontract.ReleaseEnvVar+"=web@0123abcd", "-e", BrowserEnvVar+`={"SENTRY_DSN":"https://key@errors.example/3"}`)
	for path, want := range map[string]string{
		"/":                     page,
		check.BrowserEnvPath:    BrowserEnvScript(`{"SENTRY_DSN":"https://key@errors.example/3"}`),
		appcontract.ReleasePath: "web@0123abcd",
	} {
		if code, body, err := check.HTTPFetcher.Fetch(context.Background(), base+path); err != nil || code != 200 || body != want {
			t.Errorf("%s: %d %q %v", path, code, body, err)
		}
	}
	// Given no browser values, as App Platform runs it, the file is still
	// an object: {}, never a script that ends mid-expression.
	if code, body, err := check.HTTPFetcher.Fetch(context.Background(), start("-e", appcontract.ReleaseEnvVar+"=web@0123abcd")+check.BrowserEnvPath); err != nil || code != 200 || body != BrowserEnvScript("{}") {
		t.Errorf("with no browser values: %d %q %v", code, body, err)
	}
}
