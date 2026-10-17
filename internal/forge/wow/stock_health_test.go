package wow

import (
	"encoding/json"
	"testing"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/stock"
)

// The shipped health block parses as platform tier with valid checks.
func TestStockHealthBlockIsPlatformTier(t *testing.T) {
	t.Parallel()
	raw, err := stock.FS.ReadFile("guidance/deploy/health-declared.md")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseStockGuidance(string(raw))
	if err != nil || b.Name != "deploy/health-declared" || b.Tier != TierPlatform.String() {
		t.Fatalf("parse: %s %s %v", b.Name, b.Tier, err)
	}
	var cs []conformance.CheckDeclaration
	if err := json.Unmarshal([]byte(b.ChecksJSON), &cs); err != nil || check.Validate(cs) != nil || len(cs) != 4 {
		t.Fatalf("checks: %v %+v", err, cs)
	}
	if cs[0].Name != "runtime-contract-present" || cs[0].Evaluator != "llm" || cs[0].Scope != "tree" {
		t.Fatalf("first check should assess whether this revision requires an explicit contract: %+v", cs[0])
	}
	if cs[2].Evaluator != "runtime" || cs[2].Probe != "readiness" {
		t.Fatalf("third check should be the runtime readiness probe: %+v", cs[2])
	}
	if cs[3].Evaluator != "runtime" || cs[3].Probe != "page" {
		t.Fatalf("fourth check should ask the front page a visitor opens: %+v", cs[3])
	}
	if _, bad := ParseStockGuidance("---\nname: x\ntier: gold\n---\nbody"); bad == nil {
		t.Fatal("an unknown tier must be refused")
	}
}
