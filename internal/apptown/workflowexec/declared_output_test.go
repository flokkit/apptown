package workflowexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
)

// A step that declares its output file is answered by that file and nothing
// else: absent or blank, its output is missing, and another Markdown file
// the agent wrote, or its chat text, never stands in. One that cannot be
// read makes the harvest a problem. A step that declares none keeps the
// single-Markdown-file rule. Locally and on a worker alike.
//
// Kills: the substitutes left in place when the declared file was absent,
// blank or unreadable (fallback audit F16).
func TestADeclaredOutputIsTheOutputOrMissing(t *testing.T) {
	t.Parallel()
	start := time.Now().Add(-time.Minute)
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "notes.md"), []byte("# not the report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if h := harvestWorkspace(ws, start, "chat text", "REPORT.md"); h.MissingOutput != "REPORT.md" || h.Output != "" || h.Problem != "" {
		t.Fatalf("absent declared output: %+v", h)
	}
	if err := os.WriteFile(filepath.Join(ws, "REPORT.md"), []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if h := harvestWorkspace(ws, start, "chat text", "REPORT.md"); h.MissingOutput != "REPORT.md" || h.Output != "" {
		t.Fatalf("blank declared output: %+v", h)
	}
	if h := harvestWorkspace(ws, start, "chat text", strings.Repeat("x", 300)+".md"); h.Problem == "" {
		t.Fatalf("an unreadable declared output: %+v", h)
	}
	if err := os.WriteFile(filepath.Join(ws, "REPORT.md"), []byte("# the report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if h := harvestWorkspace(ws, start, "chat text", "REPORT.md"); h.Output != "# the report\n" || h.MdFile != "REPORT.md" || h.MissingOutput != "" {
		t.Fatalf("the declared output: %+v", h)
	}
	if err := os.Remove(filepath.Join(ws, "REPORT.md")); err != nil {
		t.Fatal(err)
	}
	if h := harvestWorkspace(ws, start, "chat text", ""); h.Output != "# not the report\n" || h.MdFile != "notes.md" {
		t.Fatalf("no declared output: the single Markdown file: %+v", h)
	}

	// On a worker: the declared file as the worker returned it.
	for name, f := range map[string]execution.ResultFile{
		"absent": {State: execution.ResultAbsent},
		"blank":  {State: execution.ResultPresent, Content: []byte("\n"), Size: 1},
	} {
		h := workspaceHarvest{Output: "# not the report\n", MdFile: "notes.md"}
		if problem := applyDeclaredOutput(&h, "REPORT.md", map[string]execution.ResultFile{"output": f}, nil); problem != "" || h.MissingOutput != "REPORT.md" || h.Output != "" {
			t.Fatalf("worker, %s declared output: %+v (%s)", name, h, problem)
		}
	}
}

// The step whose declared output is missing fails and says which file.
func TestAnAgentStepWithoutItsDeclaredOutputFails(t *testing.T) {
	t.Parallel()
	a, _, stepID := admittedAgentStep(t, `{"prompt":"write the report","output":{"kind":"report","name":"r","file":".apptown/R.md"}}`)
	installFakePi(a, func(_ int, req execution.Request) execution.Outcome {
		if err := os.WriteFile(filepath.Join(req.Workspace, "NOTES.md"), []byte("# notes, not the report\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return execution.Outcome{Output: piSays("wrote some notes")}
	})
	a.work.ExecuteStep(a.ctx, stepID)
	if status, ev := stepStatus(t, a, stepID); status != workflow.StatusFail || !strings.Contains(ev["reason"].(string), ".apptown/R.md") {
		t.Fatalf("want a failure naming the declared output, got %s %v", status, ev)
	}
}
