package workflowexec

import (
	"encoding/json"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
)

func TestGoReportLogIsPlainText(t *testing.T) {
	t.Parallel()
	report := []byte(`{"Action":"output","Package":"example/app","Test":"TestFail","Output":"\u001b[31mfailed\u001b[0m\n"}
{"Action":"fail","Package":"example/app","Test":"TestFail"}
`)
	for _, prefix := range []string{"", "command\n"} {
		evidence := map[string]any{}
		got := recordTestReport(evidence, execution.Outcome{Report: report}, prefix)
		if evidence["testReportFormat"] != "go test -json" || got != prefix+"failed\n" {
			t.Fatalf("decoded log %q, evidence %#v", got, evidence)
		}
	}
}

func TestScriptTaskRecordsPlainOutput(t *testing.T) {
	t.Parallel()
	a, _, step := scriptTaskFixture(t, map[string]any{
		"run": "true", "executor": "docker", "image": "fake",
		"env": map[string]any{"FAKE_OUTPUT": "\x1b[32mok\x1b[0m\n", "FAKE_EXIT": "0"},
	})
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	got, err := workflow.ReadStepIn(a.ctx, a.db, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		Log string `json:"log"`
	}
	if err := json.Unmarshal(got.Evidence, &evidence); err != nil {
		t.Fatal(err)
	}
	if got.Status != workflow.StatusPass || evidence.Log != "ok\n" {
		t.Fatalf("recorded script status %s, evidence %s", got.Status, got.Evidence)
	}
}
