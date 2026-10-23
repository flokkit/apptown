//go:build integration

package docker

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
)

// On a real daemon: a step's throwaway service keeps its data in an
// anonymous volume, and the stack's teardown deletes that volume with the
// container. A container started with --rm and removed with rm -f keeps
// it, which is how thousands of them were left.
func TestIntegrationAThrowawayServiceLeavesNoVolume(t *testing.T) {
	dk := Scope{Client: CLI{Binary: "docker"}}
	stack := fmt.Sprintf("itown-vol-%d", time.Now().UnixNano()%1_000_000)
	_, teardown, err := StartServices(context.Background(), dk, stack, []deploy.ServiceSpec{
		{Name: "store", Image: "alpine:3", Data: "/data", Command: []string{"sleep", "3600"}},
	})
	t.Cleanup(teardown)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("docker", "inspect", "--format", `{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}`, Current.Service(stack, "store")).CombinedOutput()
	volume := strings.TrimSpace(string(out))
	if err != nil || volume == "" {
		t.Fatalf("the service's data volume: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", volume).Run() })
	teardown()
	if out, err := exec.Command("docker", "volume", "inspect", volume).CombinedOutput(); err == nil {
		t.Fatalf("the throwaway service's anonymous volume %s outlived it: %s", volume, out)
	}
}
