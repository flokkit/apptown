//go:build integration

package docker

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	testinputs "github.com/flokkit/apptown/internal/execution/executiontest"
	"github.com/flokkit/apptown/internal/poll"
)

// The app's actual allow-list consumes the exact rendered environment, accepting
// its own host while refusing an unrelated preview and a sibling deployment.
func TestIntegrationDockerExactOriginsRejectSiblingHosts(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	suffix := fmt.Sprint(time.Now().UnixNano())
	name, image := "apptown-origin-it-"+suffix, "apptown-origin-it:"+suffix
	dk := Scope{Client: CLI{Binary: "docker"}}
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		if err := dk.RemoveDisposable(clean, name); err != nil {
			t.Errorf("remove owned app: %v", err)
		}
		if out, err := exec.CommandContext(clean, "docker", "image", "rm", "-f", image).CombinedOutput(); err != nil {
			t.Errorf("remove owned image: %v %s", err, out)
		}
	})
	testinputs.Files(t, filepath.Join("testdata", "sealed-app"))
	if out, err := exec.CommandContext(ctx, "docker", "build", "-q", "-t", image, filepath.Join("testdata", "sealed-app")).CombinedOutput(); err != nil {
		t.Fatalf("build owned app: %v %s", err, out)
	}
	imageID := dk.Text(ctx, "image", "inspect", "-f", "{{.Id}}", image)
	if imageID == "" {
		t.Fatal("built fixture image has no identity")
	}
	origin := "https://original.apps.example"
	port, err := NewHost(Dependencies{StaticServer: testStaticServer, DFImage: testDF, Routes: liveRoutes{}}).startAppContainer(ctx, BootRequest{Allowed: AuthorisedWhenRequested, Name: name, Workspace: t.TempDir(), Docker: dk, Slug: "renamed", ExactOrigins: &origin, Recorded: &RecordedImages{Parts: map[string]string{"web": imageID}}}, &appcontract.Part{Image: imageID, Port: 8080}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	status := func(host, path string) int {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, dk.ProbeURL(port)+path, nil)
		if err != nil {
			return 0
		}
		req.Host = host
		res, err := client.Do(req)
		if err != nil {
			return 0
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	if !poll.Until(ctx, 20*time.Second, 100*time.Millisecond, func() bool { return status("original.apps.example", "/healthz") == 200 }) {
		t.Fatal("owned app never became ready")
	}
	for host, want := range map[string]int{"original.apps.example": 200, "sibling.apps.example": 403, "change-1.previews.example": 403, "renamed.apps.example": 403} {
		if got := status(host, "/"); got != want {
			t.Errorf("host %s answered %d want %d", host, got, want)
		}
	}
}
