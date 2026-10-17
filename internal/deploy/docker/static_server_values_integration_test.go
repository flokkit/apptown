//go:build integration

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/poll"
)

// Values the page is given arrive intact through the real static server and
// read back as the same values in JavaScript, however hostile their text
// (script-closing tags, line separators, shell and placeholder look-alikes);
// with nothing given, the page gets an empty object.
func TestIntegrationStaticServerKeepsValuesIntact(t *testing.T) {
	values := map[string]string{"PUBLIC": "</script><script>alert('x')</script>\"\n\u2028\u2029 & ' $HOME $(id) `id` {env.PRIVATE_CANARY}", "UNICODE": "😀"}
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{string(raw), ""} {
		t.Run(fmt.Sprintf("bytes-%d", len(value)), func(t *testing.T) {
			args := []string{"run", "-d", "-p", "127.0.0.1::8080", "-e", "PRIVATE_CANARY=should-not-expand;FORGE_BROWSER_ENV=canary", "-e", "PREFIX_FORGE_BROWSER_ENV=canary", "-e", "FORGE_BROWSER_ENV_SUFFIX=canary"}
			if value != "" {
				args = append(args, "-e", BrowserEnvVar+"="+value)
			}
			args = append(args, StaticServerImage)
			args = append(args, StaticServerCommand(8080)...)
			output, err := exec.Command("docker", args...).CombinedOutput()
			if err != nil {
				t.Fatalf("launch: %s %v", output, err)
			}
			id := strings.TrimSpace(string(output))
			defer exec.Command("docker", "rm", "-f", id).Run()
			port, err := exec.Command("docker", "port", id, "8080/tcp").Output()
			if err != nil {
				t.Fatal(err)
			}
			url := "http://" + strings.TrimSpace(strings.Split(string(port), "\n")[0]) + check.BrowserEnvPath
			var body string
			var code int
			poll.Until(context.Background(), 15*time.Second, 100*time.Millisecond, func() bool {
				code, body, err = check.HTTPFetcher.Fetch(context.Background(), url)
				return err == nil && code == 200
			})
			if err != nil || code != 200 {
				logs, _ := exec.Command("docker", "logs", id).CombinedOutput()
				t.Fatalf("HTTP %d %v logs %s", code, err, logs)
			}
			// What the container was started with, read back through the one
			// narrow inspection: the values alone, never another variable.
			installed, err := InspectBrowserValues(context.Background(), Scope{Client: CLI{Binary: "docker"}}, id)
			switch {
			case value == "" && !errors.Is(err, ErrNoBrowserValues):
				t.Fatalf("a container given nothing must say so: %+v %v", installed, err)
			case value != "" && (err != nil || !reflect.DeepEqual(installed.Values, values) || !strings.HasPrefix(id, installed.ID)):
				t.Fatalf("installed values: %+v %v", installed, err)
			}
			want := value
			if want == "" {
				want = "{}" // unset, as App Platform leaves it: still a value
			}
			if body != BrowserEnvScript(want) {
				t.Fatalf("body changed during Caddy replacement: %q", body)
			}
			cmd := exec.Command("node", "-e", `const fs=require('fs');const vm=require('vm');const ctx={window:{}};vm.runInNewContext(fs.readFileSync(0,'utf8'),ctx);process.stdout.write(JSON.stringify(ctx.window.__FORGE_ENV__));`)
			cmd.Stdin = strings.NewReader(body)
			got, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("JavaScript: %s %v", got, err)
			}
			var decoded map[string]string
			if err = json.Unmarshal(got, &decoded); err != nil {
				t.Fatal(err)
			}
			if value == "" {
				if len(decoded) != 0 {
					t.Fatal(decoded)
				}
			} else if !reflect.DeepEqual(decoded, values) {
				t.Fatalf("JS values changed: %#v", decoded)
			}
		})
	}
}
