package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// A workflow's services, carried in the prepared value, are started by the
// worker's boot beside the app: the worker boots what was prepared, not
// only what the contract declares. (This Docker runs nothing; the boot's
// own result does not matter here.)
func TestTheWorkerStartsAWorkflowsServicesFromThePreparedValue(t *testing.T) {
	t.Parallel()
	dk, _ := sweepDocker(t)
	dk.reply = func(args []string) ([]byte, error, bool) {
		line := strings.Join(args, " ")
		switch {
		case strings.Contains(line, "context show"):
			return []byte("default\n"), nil, true
		case strings.Contains(line, ".State.Health"):
			return []byte("healthy\n"), nil, true
		case strings.Contains(line, "{{.State.Running}} {{.Id}}"):
			return []byte("Error: No such object\n"), errExit, true
		}
		return nil, nil, true
	}
	ca := workertest.NewCA(t)
	hs := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Previews: dk})
	previews := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}

	c, err := appcontract.Parse([]byte("version: 1\napp:\n  web:\n    static: .\n    public: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	prepared := docker.Prepared{Version: docker.PreparedVersion, Contract: c, BrowserEnv: "{}",
		Services: []deploy.ServiceSpec{{Name: "cache", Image: "forge-test-cache:7", Health: "true"}}}
	if _, err := previews.Boot(context.Background(), worker.PreviewSpec{Name: "apptown-preview-run-5", StackID: "pv55", Slug: "app",
		Incarnation: "tree+digest", ProbeSeconds: 1}, prepared, tree); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(dk.commands(""), "\n")
	var started bool
	for _, line := range dk.commands("") {
		if strings.Contains(line, " run ") || strings.HasPrefix(line, "run ") || strings.Contains(line, "run -d") {
			if strings.Contains(line, "forge-test-cache:7") && strings.Contains(line, "pv55") {
				started = true
			}
		}
	}
	if !started {
		t.Fatalf("the worker did not start the workflow's service:\n%s", body)
	}
}
