package forge

import (
	"encoding/json"
	"fmt"
	"testing"

	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestPolicyLibraryHTTPPinsAdoptionAndReportsUsage(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	seedLibraryWorkflow(t, a, idstest.N(1), "review", `apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: {name: review}
spec: {kind: lifecycle, stages: [{name: work, steps: [{name: accept, uses: approval, with: {means: "reviewed"}}]}]}
`)
	declaration := `{"name":"needs-mail","description":"mail is available","rule":"requireCapabilityBound","with":{"capability":"mailer"}}`
	for _, tc := range []struct {
		who, body string
		code      int
	}{{"outsider", declaration, 403}, {"boss", `{`, 400}, {"boss", `{"name":"bad","rule":"invented"}`, 400}} {
		if w := authzCall(h, tc.who, "POST", "/api/v1/policy-library", tc.body); w.Code != tc.code {
			t.Fatalf("policy refusal: %d %s", w.Code, w.Body.String())
		}
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/policy-library", declaration); w.Code != 200 {
		t.Fatalf("save policy: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/policies", `{"fromLibrary":"needs-mail"}`); w.Code != 201 {
		t.Fatalf("adopt: %d %s", w.Code, w.Body.String())
	}
	w := authzCall(h, "owner", "GET", "/api/v1/policy-library", "")
	var out struct {
		Library []struct {
			wow.LibraryPolicy
			AdoptedBy, NonConformant int
			Packs                    []string
			Reading, Fix             string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || len(out.Library) != 1 {
		t.Fatalf("library: %d %s %v", w.Code, w.Body.String(), err)
	}
	p := out.Library[0]
	if p.Name != "needs-mail" || p.AdoptedBy != 1 || p.NonConformant != 1 || p.Packs == nil || p.Reading == "" || p.Fix == "" {
		t.Fatalf("policy projection: %+v", p)
	}
	w = authzCall(h, "owner", "GET", "/api/v1/policy-library/needs-mail/usage", "")
	var usage struct {
		UsedBy []struct {
			Slug       string
			Conformant bool
			Violations []adoption.Violation
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &usage); err != nil || w.Code != 200 || len(usage.UsedBy) != 1 || usage.UsedBy[0].Slug != "game" || usage.UsedBy[0].Conformant || len(usage.UsedBy[0].Violations) == 0 {
		t.Fatalf("usage: %d %s %v", w.Code, w.Body.String(), err)
	}
	if w := authzCall(h, "outsider", "DELETE", "/api/v1/policy-library/needs-mail", ""); w.Code != 403 {
		t.Fatalf("foreign deletion: %d", w.Code)
	}
	if w := authzCall(h, "boss", "DELETE", "/api/v1/policy-library/needs-mail", ""); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", "DELETE", "/api/v1/policy-library/needs-mail", ""); w.Code != 404 {
		t.Fatalf("repeat delete: %d", w.Code)
	}
	w = authzCall(h, "owner", "GET", "/api/v1/projects/game/policies", "")
	var selected struct {
		Policies   []adoption.Policy
		Violations []adoption.Violation
	}
	if err := json.Unmarshal(w.Body.Bytes(), &selected); err != nil || w.Code != 200 || len(selected.Policies) != 1 || selected.Policies[0].Name != "needs-mail" || selected.Policies[0].Rule != "requireCapabilityBound" || len(selected.Violations) == 0 {
		t.Fatalf("deleting library lost adopted rule: %d %s %v", w.Code, w.Body.String(), err)
	}
	// The project's copy is removed by id; removing it twice is not found,
	// which the owner says without the handler reading the store's error.
	path := fmt.Sprintf("/api/v1/policies/%d", selected.Policies[0].ID)
	for _, code := range []int{200, 404} {
		if w := authzCall(h, "boss", "DELETE", path, ""); w.Code != code {
			t.Fatalf("remove project policy: want %d, got %d %s", code, w.Code, w.Body.String())
		}
	}
}
