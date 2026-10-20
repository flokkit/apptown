package verification

import (
	"encoding/json"
	"testing"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/workflow"
)

func TestCheckReceiptKeepsZeroValuesAndExactProvenance(t *testing.T) {
	t.Parallel()
	sc := conformance.DeclaredCheck{Block: "hygiene", Version: 2, Check: conformance.CheckDeclaration{Name: "clean", Severity: "high"}}
	results := Results{{Block: sc.Block, Version: 1, Check: sc.Check.Name, Evaluator: "llm", Verdict: workflow.VerdictFail},
		{Block: sc.Block, Version: 2, Check: sc.Check.Name, Evaluator: "sh", Verdict: workflow.VerdictPass, ExitCode: new(0), DurationMs: new(int64(0)), Gates: new(false)},
	}
	f := conformance.Finding{Finding: check.Finding{Fingerprint: conformance.Fingerprint(sc, "a.txt")}}
	if findingEvaluator(results, f) != "sh" {
		t.Fatal("same-named checks from different versions must not share an evaluation")
	}
	if findingEvaluator(results, conformance.Finding{Finding: check.Finding{Fingerprint: "other|hygiene@2|clean|"}}) != "" {
		t.Fatal("another finding kind must not inherit a guidance evaluator")
	}
	data, err := json.Marshal(results[1])
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["exitCode"] != float64(0) || wire["durationMs"] != float64(0) || wire["gates"] != false {
		t.Fatalf("zero/false observations must survive JSON: %s", data)
	}
	if _, exists := wire["apptownFault"]; exists {
		t.Fatalf("absent fault must stay absent: %s", data)
	}
}
