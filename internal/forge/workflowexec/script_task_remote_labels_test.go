package workflowexec

import (
	"context"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/tasks"
)

type remoteStub struct{}

func (remoteStub) Collect(context.Context, string, string, string, string) (string, error) {
	panic("not called")
}

// A remote task's spec does not name the forge's folder as one it writes
// (the worker makes its own checkout), keeps its step label, and leaves the
// local request's labels as they were.
func TestARemoteTaskDoesNotClaimTheForgesFolder(t *testing.T) {
	plan := scriptPlan{repository: "repo", request: execution.Request{Workspace: "/controller/change", Mounts: []string{"/host:/container"}, ExtraHosts: []string{"host:1.2.3.4"}, Labels: []string{"forge.writes=/controller/change", "forge.step=42"}}}
	a := Commands{tasks: &TaskRunner{Remote: remoteStub{}}}
	attempt := tasks.Attempt{}
	attempt.Source = "commit"
	got := a.taskSpec(attempt, plan)
	if got.Workspace != "" || len(got.Mounts) > 0 || len(got.ExtraHosts) > 0 || got.Source == nil || got.Source.Commit != "commit" {
		t.Fatalf("remote request: %+v", got)
	}
	if slices.ContainsFunc(got.Labels, func(l string) bool { return strings.HasPrefix(l, "forge.writes=") }) || !slices.Contains(got.Labels, "forge.step=42") {
		t.Fatalf("labels: %v", got.Labels)
	}
	if !slices.Contains(plan.request.Labels, "forge.writes=/controller/change") {
		t.Fatal("mutated source labels")
	}
}
