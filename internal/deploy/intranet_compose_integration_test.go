//go:build integration

package deploy_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/execution"
	"gopkg.in/yaml.v3"
)

type emptyComposeSecrets struct{}

func (emptyComposeSecrets) EnvFor(string) map[string]string { return nil }

// The generated graph is exercised by Compose, not reconstructed by this test.
// The extra fixture volume exposes only test markers; it is not app dataDir support.
func TestIntegrationPortainerComposeOrdersInitAndBlocksWorkersOnFailure(t *testing.T) {
	const image = "busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
	command := func(ctx context.Context, args ...string) execution.Outcome {
		t.Helper()
		return (execution.Runner{}).Run(ctx, execution.Local, execution.Request{Argv: append([]string{"docker"}, args...), Timeout: 90 * time.Second})
	}
	if got := command(t.Context(), "compose", "version"); !got.Exited0() {
		t.Fatalf("Compose required for runtime qualification: %s %v", got.Output, got.RunErr)
	}
	for _, fails := range []bool{false, true} {
		t.Run(fmt.Sprintf("first-init-fails=%t", fails), func(t *testing.T) {
			name := fmt.Sprintf("apptown-init-proof-%d", time.Now().UnixNano())
			volume := name + "-markers"
			file := filepath.Join(t.TempDir(), "compose.yaml")
			compose := func(ctx context.Context, args ...string) execution.Outcome {
				return command(ctx, append([]string{"compose", "--project-name", name, "--file", file}, args...)...)
			}
			first := "sleep 1; echo first >> /proof/events; touch /proof/first"
			if fails {
				first = "sleep 1; exit 17"
			}
			st := deploy.IntranetStack{Secrets: emptyComposeSecrets{}, Prefix: "apptown-", Base: name, Hosts: []string{"proof.invalid"}, Domain: "invalid", Image: image, Port: 8080,
				Command: []string{"sh", "-c", "test -f /proof/second || exit 42; echo app >> /proof/events; sleep 300"},
				Init: []appcontract.InitContainer{
					{Name: "first", Image: image, Command: []string{"sh", "-c", first}},
					{Name: "second", Image: image, Command: []string{"sh", "-c", "test -f /proof/first || exit 41; sleep 1; echo second >> /proof/events; touch /proof/second"}},
				},
				Parts: []appcontract.Part{{Name: "worker", Image: image, Command: []string{"sh", "-c", "test -f /proof/second || exit 43; echo worker >> /proof/events; sleep 300"}}}}
			var document map[string]any
			if err := yaml.Unmarshal([]byte(deploy.IntranetCompose(st)), &document); err != nil {
				t.Fatal(err)
			}
			for _, service := range document["services"].(map[string]any) {
				service.(map[string]any)["volumes"] = []string{"proof:/proof"}
			}
			document["volumes"] = map[string]any{"proof": map[string]any{"name": volume}}
			raw, err := yaml.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				if got := command(ctx, "rm", "--force", name+"-read"); !got.Exited0() && !strings.Contains(string(got.Output), "No such container") {
					t.Errorf("cleaning exact marker reader: %s %v", got.Output, got.RunErr)
				}
				if got := compose(ctx, "down", "--volumes", "--remove-orphans", "--timeout", "3"); !got.Exited0() {
					t.Errorf("cleaning exact fixture %s: %s %v", name, got.Output, got.RunErr)
				}
			})
			read := func() string {
				t.Helper()
				got := command(t.Context(), "run", "--rm", "--name", name+"-read", "--network", "none", "-v", volume+":/proof:ro", image, "sh", "-c", "cat /proof/events 2>/dev/null || true")
				if !got.Exited0() {
					t.Fatalf("read markers: %s %v", got.Output, got.RunErr)
				}
				return string(got.Output)
			}
			got := compose(t.Context(), "up", "--detach", "--wait-timeout", "45")
			if fails {
				if got.Exited0() {
					t.Fatal("failed init did not refuse application startup")
				}
				if events := read(); events != "" {
					t.Fatalf("a dependent ran after failed first init: %q", events)
				}
				return
			}
			if !got.Exited0() {
				t.Fatalf("start generated graph: %s %v", got.Output, got.RunErr)
			}
			deadline := time.Now().Add(15 * time.Second)
			for {
				events := read()
				if strings.Contains(events, "worker\n") && strings.Contains(events, "app\n") {
					if !strings.HasPrefix(events, "first\nsecond\n") {
						t.Fatalf("application started before ordered init: %q", events)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("application did not start after init: %q", events)
				}
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-t.Context().Done():
					timer.Stop()
					t.Fatal(t.Context().Err())
				case <-timer.C:
				}
			}
			// Same generated declaration, another deployment: require new init
			// execution, not the successful containers/markers from the first.
			if got := compose(t.Context(), "up", "--detach", "--wait-timeout", "45"); !got.Exited0() {
				t.Fatalf("repeat deploy: %s %v", got.Output, got.RunErr)
			}
			if events := read(); strings.Count(events, "first\n") != 2 || strings.Count(events, "second\n") != 2 {
				t.Fatalf("repeat deployment reused old init evidence: %q", events)
			}
		})
	}
}
