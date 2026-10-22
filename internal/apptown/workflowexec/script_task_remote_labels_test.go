package workflowexec

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/execution"
)

type remoteStub struct{}

func (remoteStub) Collect(context.Context, string, string, string, string) (string, error) {
	panic("not called")
}

// A remote task's spec does not name AppTown's folder as one it writes
// (the worker makes its own checkout), keeps its step label, and leaves the
// local request's labels as they were.
func TestARemoteTaskDoesNotClaimAppTownsFolder(t *testing.T) {
	plan := scriptPlan{repository: "repo", request: execution.Request{Workspace: "/controller/change", Mounts: []string{"/host:/container"}, ExtraHosts: []string{"host:1.2.3.4"}, Labels: []string{"apptown.writes=/controller/change", "apptown.step=42"}}}
	a := Commands{tasks: &TaskRunner{Remote: remoteStub{}}}
	attempt := tasks.Attempt{}
	attempt.Source = "commit"
	got := a.taskSpec(attempt, plan)
	if got.Workspace != "" || len(got.Mounts) > 0 || len(got.ExtraHosts) > 0 || got.Source == nil || got.Source.Commit != "commit" {
		t.Fatalf("remote request: %+v", got)
	}
	if slices.ContainsFunc(got.Labels, func(l string) bool { return strings.HasPrefix(l, "apptown.writes=") }) || !slices.Contains(got.Labels, "apptown.step=42") {
		t.Fatalf("labels: %v", got.Labels)
	}
	if !slices.Contains(plan.request.Labels, "apptown.writes=/controller/change") {
		t.Fatal("mutated source labels")
	}
}
