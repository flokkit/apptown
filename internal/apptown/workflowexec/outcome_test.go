package workflowexec

import (
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// A passing agent step hands Work its typed output (announced, named for the
// step unless configured) and every file it wrote, the markdown file that is
// the output only once, so both are recorded with the pass.
func TestAgentArtifactsGoWithThePassingOutcome(t *testing.T) {
	t.Parallel()
	files := workspaceHarvest{Output: "# spec", MdFile: "spec.md", Files: []wroteFile{{rel: "spec.md", body: "# spec"}, {rel: "notes.txt", body: "n"}}}
	typed := agentConfig{}
	typed.Output.Kind = "spec"
	got := agentArtifacts(&typed, files, "draft")
	want := []workflow.StepArtifact{
		{ArtifactDraft: workflow.ArtifactDraft{Kind: "spec", Name: "draft", Body: "# spec"}, By: "agent:draft"},
		{ArtifactDraft: workflow.ArtifactDraft{Kind: "file", Name: "notes.txt", Body: "n"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("typed output:\n got %+v\nwant %+v", got, want)
	}
	untyped := agentArtifacts(&agentConfig{}, files, "draft")
	want = []workflow.StepArtifact{
		{ArtifactDraft: workflow.ArtifactDraft{Kind: "file", Name: "spec.md", Body: "# spec"}},
		{ArtifactDraft: workflow.ArtifactDraft{Kind: "file", Name: "notes.txt", Body: "n"}},
	}
	if !reflect.DeepEqual(untyped, want) {
		t.Fatalf("no typed output:\n got %+v\nwant %+v", untyped, want)
	}
}

// A typed output and a written file that share a name are different
// artifacts: only the markdown file that stood in for the output is folded
// into it, so a report named result.txt and a written result.txt both go
// with the pass, each with its own body.
func TestAgentArtifactsKeepATypedOutputAndAFileOfTheSameName(t *testing.T) {
	t.Parallel()
	files := workspaceHarvest{Output: "the report", Files: []wroteFile{{rel: "result.txt", body: "the file"}}}
	typed := agentConfig{}
	typed.Output.Kind, typed.Output.Name = "report", "result.txt"
	got := agentArtifacts(&typed, files, "draft")
	want := []workflow.StepArtifact{
		{ArtifactDraft: workflow.ArtifactDraft{Kind: "report", Name: "result.txt", Body: "the report"}, By: "agent:draft"},
		{ArtifactDraft: workflow.ArtifactDraft{Kind: "file", Name: "result.txt", Body: "the file"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("same-named artifacts:\n got %+v\nwant %+v", got, want)
	}
}
