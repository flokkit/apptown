//go:build integration

package execution_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
)

func TestCheckSourceIsReadOnlyWhileItsWorkIsWritable(t *testing.T) {
	src, work := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "source"), []byte("committed"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := execution.DockerTasks{Namespace: "check-source-test", Daemon: execution.SelectDocker(), CallerAdmitsDisk: true}
	spec := execution.TaskSpec{Key: "read-only-source", Image: "alpine:3.20", Workspace: work, ReadOnlySource: src, Argv: []string{"sh", "-c", "if echo changed > /src/source; then exit 42; fi; cat /src/source > /work/answer"}, Admission: execution.Admission{Profile: "local-docker", Network: execution.NetworkNone, Limits: execution.Limits{WallClock: time.Minute}}}
	obs, err := d.Ensure(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: spec.Key, ProviderID: obs.ID}
	t.Cleanup(func() { _ = d.Release(context.Background(), ref) })
	if err := d.Start(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	exit, err := d.Wait(t.Context(), ref)
	if err != nil || exit != 0 {
		t.Fatalf("task: exit=%d %v", exit, err)
	}
	b, err := os.ReadFile(filepath.Join(work, "answer"))
	if err != nil || string(b) != "committed" {
		t.Fatalf("scratch: %q %v", b, err)
	}
}
