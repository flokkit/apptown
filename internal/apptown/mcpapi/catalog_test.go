package mcpapi

import "testing"

func TestCatalogRejectsUnboundPoliciesBeforeInvocation(t *testing.T) {
	t.Parallel()
	run := func(*Call, map[string]any) (any, error) {
		t.Fatal("invalid tool reached its command")
		return nil, nil
	}
	for _, tool := range []toolEntry{
		{Name: "missing-area", command: ownerCommand("test", run)},
		{Name: "missing", area: "projects"},
		{Name: "unnamed-contract", area: "projects", command: ownerCommand("", run)},
		{Name: "unknown-policy", area: "projects", command: toolCommand{"unknown", "test", run}},
	} {
		t.Run(tool.Name, func(t *testing.T) {
			if _, err := tool.Run(&Call{}, nil); err == nil {
				t.Fatal("policy-less invocation accepted")
			}
			defer func() {
				if recover() == nil {
					t.Fatal("invalid catalogue constructed")
				}
			}()
			Assemble([]toolEntry{tool})
		})
	}
}
