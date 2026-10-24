//go:build integration

package docker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/runtimeimages"
)

// The real static server, as every path runs it: the site from /srv, and
// /_apptown/env.js carrying the values it was started with, which the page can
// load. Unset, the file answers an empty object, {}. Judged by the same
// check the apptown runs against a preview.
func TestIntegrationStaticServerGivesThePageItsValues(t *testing.T) {
	site := t.TempDir()
	page := `<html><script src="/_apptown/env.js"></script><script src="game.js"></script></html>`
	if err := os.WriteFile(filepath.Join(site, "index.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	start := func(values string) string {
		t.Helper()
		args := []string{"run", "-d", "-p", "127.0.0.1::8080", "-v", site + ":/srv:ro"}
		if values != "" {
			args = append(args, "-e", BrowserEnvVar+"="+values)
		}
		args = append(args, runtimeimages.StaticServer())
		args = append(args, StaticServerCommand(8080)...)
		id := runDetached(t, args...)
		port, err := exec.Command("docker", "port", id, "8080/tcp").CombinedOutput()
		if err != nil {
			state, _ := exec.Command("docker", "inspect", "-f", "{{json .State}} {{json .NetworkSettings.Ports}}", id).CombinedOutput()
			logs, _ := exec.Command("docker", "logs", id).CombinedOutput()
			t.Fatalf("docker port: %v %s\nthe server's state: %s\nits log:\n%s", err, port, state, logs)
		}
		base := "http://" + strings.TrimSpace(strings.Split(string(port), "\n")[0])
		if !poll.Until(context.Background(), 30*time.Second, 100*time.Millisecond, func() bool {
			code, _, err := check.HTTPFetcher.Fetch(context.Background(), base+"/")
			return err == nil && code == 200
		}) {
			logs, _ := exec.Command("docker", "logs", id).CombinedOutput()
			t.Fatalf("the static server did not answer: %s", logs)
		}
		return base
	}

	base := start(`{"SENTRY_DSN":"https://key@errors.example/3"}`)
	code, body, err := check.HTTPFetcher.Fetch(context.Background(), base+check.BrowserEnvPath)
	if err != nil || code != 200 || body != BrowserEnvScript(`{"SENTRY_DSN":"https://key@errors.example/3"}`) {
		t.Fatalf("env.js: %d %q %v", code, body, err)
	}
	given := map[string]string{"SENTRY_DSN": "https://key@errors.example/3"}
	if v := check.JudgeBrowserEnv(context.Background(), check.HTTPFetcher, base, check.BrowserExpectation{Static: true, Attached: given, Installed: given}); v.Outcome != check.Conforms || !strings.Contains(v.Detail, "SENTRY_DSN") {
		t.Fatalf("judged: %+v", v)
	}
	if code, body, _ := check.HTTPFetcher.Fetch(context.Background(), base+"/"); code != 200 || body != page {
		t.Fatalf("the site itself: %d %q", code, body)
	}

	bare := start("")
	if code, body, _ := check.HTTPFetcher.Fetch(context.Background(), bare+check.BrowserEnvPath); code != 200 || body != BrowserEnvScript("{}") {
		t.Fatalf("with nothing given: %d %q", code, body)
	}
}

// The static server starts without a warning: its Caddyfile is formatted as
// caddy fmt writes it, and it says it serves plain HTTP/1.1, so a person
// reading a deployment's log sees nothing that looks like a fault.
//
// Kills: the global options block removed, or the closing newline.
func TestIntegrationStaticServerStartsWithoutWarnings(t *testing.T) {
	site := t.TempDir()
	if err := os.WriteFile(filepath.Join(site, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"run", "-d", "-v", site + ":/srv:ro", runtimeimages.StaticServer()}, StaticServerCommand(8080)...)
	id := runDetached(t, args...)
	var logs []byte
	if !poll.Until(context.Background(), 30*time.Second, 100*time.Millisecond, func() bool {
		logs, _ = exec.Command("docker", "logs", id).CombinedOutput()
		return strings.Contains(string(logs), "serving initial configuration")
	}) {
		t.Fatalf("the static server did not start: %s", logs)
	}
	for _, line := range strings.Split(string(logs), "\n") {
		if strings.Contains(line, `"level":"warn"`) || strings.Contains(line, `"level":"error"`) {
			t.Errorf("the static server logged: %s", line)
		}
	}
}

// runDetached runs docker with args (a "run -d") and returns the new
// container's id, removed when the test ends. The id is docker's standard
// output alone: when the image is not here yet, docker reports its pull on
// standard error, and that text is not part of the id (22 Oct: the pull's
// lines were read as the id, and docker port answered "page not found").
func runDetached(t *testing.T, args ...string) string {
	t.Helper()
	var stderr strings.Builder
	cmd := exec.Command("docker", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker %s: %v %s", strings.Join(args, " "), err, stderr.String())
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
	return id
}
