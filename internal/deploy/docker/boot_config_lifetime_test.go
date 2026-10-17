package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy/appcontract"
)

type reviewBlockedBuild struct {
	entered chan string
	resume  chan struct{}
}

func (c *reviewBlockedBuild) Run(ctx context.Context, host string, req Command) ([]byte, error) {
	args := req.Args
	config := ""
	if len(args) > 1 && args[0] == "--config" {
		config = args[1]
		args = args[2:]
	}
	if len(args) > 0 && args[0] == "build" {
		c.entered <- config
		<-c.resume
	}
	return (&recordingClient{}).Run(ctx, host, req)
}

// Advance only the config's age while the real Host.Boot call is still in
// a build. Builds/init commands have no deadline, so this is reachable.
func TestAnotherBootNeverRemovesARunningBootsConfiguration(t *testing.T) {
	scratch := t.TempDir()
	c, err := appcontract.Parse([]byte("version: 1\napp:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	prepared := Prepared{Version: PreparedVersion, Contract: c}
	client := &reviewBlockedBuild{entered: make(chan string, 1), resume: make(chan struct{})}
	finished := make(chan error, 1)
	workspace := t.TempDir()
	go func() {
		_, err := NewHost(prepared.Dependencies(upProbe{}, "", "")).Boot(context.Background(), BootRequest{
			Docker: Scope{Client: client, Host: "unix:///review"}, Name: "forge-preview-change-42", StackID: "pv42", Workspace: workspace, Scratch: scratch, Probe: time.Second,
		}, c)
		finished <- err
	}()
	config := <-client.entered
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(config, old, old); err != nil {
		t.Fatal(err)
	}
	_, cleanup, err := (Scope{Client: &recordingClient{}, Host: "unix:///review"}).WithBootConfig(t.Context(), scratch, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	_, statErr := os.Stat(filepath.Join(config, "config.json"))
	close(client.resume)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if statErr != nil {
		t.Fatalf("another boot swept an active build's Docker config: %v", statErr)
	}
}
