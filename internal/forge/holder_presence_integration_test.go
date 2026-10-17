//go:build integration

package forge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockertest"
	"flokkit.io/forge/internal/forge/workflow"
)

// Against the real Docker daemon: a container left labelled as writing a
// change's folder is found when a new worker takes the change, removed, and
// only then is the worker admitted.
func TestIntegrationTakingAChangeRemovesWhatStillWritesItsFolder(t *testing.T) {
	w := newHolderWorld(t)
	w.a.docker.Client = docker.CLI{} // the real daemon
	rebuildRoutes(w.a)               // the routes read the App's Docker
	name := fmt.Sprintf("forge-folder-writer-%d-%d", os.Getpid(), w.change.ID)
	out, err := dockertest.CombinedOutput("run", "-d", "--rm", "--name", name, "--label", docker.WritesLabel+"="+w.folder, "busybox:latest", "sleep", "300")
	if err != nil {
		t.Fatalf("starting a leftover writer: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = dockertest.Run("rm", "-f", name) })
	if _, err := w.a.Work.StartRun(asPerson("owner"), workflow.StartChangeRun{ChangeID: w.change.ID, Workflow: "implement"}); err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	listed, err := dockertest.CombinedOutput("ps", "-a", "--format", "{{.Names}}", "--filter", "label="+docker.WritesLabel+"="+w.folder)
	if err != nil {
		t.Fatalf("listing: %v: %s", err, listed)
	}
	if strings.TrimSpace(string(listed)) != "" {
		t.Fatalf("the change was taken while Docker still lists its folder's writer: %s", listed)
	}
	if clear, err := holderPresenceFor(w.a).FolderClear(context.Background(), w.change.ID); err != nil || !clear {
		t.Fatalf("the real daemon does not confirm the folder clear: %v %v", clear, err)
	}
	if !strings.HasPrefix(w.holder(t), "run:") {
		t.Fatalf("the run did not take the change: %s", w.holder(t))
	}
}
