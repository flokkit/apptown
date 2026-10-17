package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/execution/executionfake"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/worker/workertest"
)

func TestReviewUnknownInputCreatesNoWorkspace(t *testing.T) {
	ca := workertest.NewCA(t)
	state := t.TempDir()
	fake := &executionfake.Tasks{}
	server := workertest.Start(t, ca, state, fake)
	driver := workertest.Driver(t, ca, server)
	hub := sourceRepo(t)
	base, err := gitexec.Output(t.Context(), hub, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	spec := fakeSpec("unknown-input-source", "", 0, false)
	spec.Source = &execution.TaskSource{Repository: hub, Commit: base}
	spec.Inputs = []execution.Input{{Blob: strings.Repeat("0", 64), Path: "/forge/in/missing", Mode: 0644}}
	if _, err := driver.Ensure(context.Background(), spec); err == nil {
		t.Fatal("unknown blob admitted")
	}
	for _, dir := range []string{filepath.Join(state, "workspaces", spec.Key), filepath.Join(state, "git", spec.Key+".git")} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("input refusal left workspace state %s: %v", dir, err)
		}
	}
}
