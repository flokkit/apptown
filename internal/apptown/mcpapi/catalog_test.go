package mcpapi

import "testing"

func TestCatalogRejectsUnboundPoliciesBeforeInvocation(t *testing.T) {
	t.Parallel()
	run := func(*Call, map[string]any) (any, error) {
		t.Fatal("invalid tool reached its command")
		return nil, nil
	}
	for _, tool := range []toolEntry{
		{Name: "missing"},
		{Name: "unnamed-contract", command: ownerCommand("", run)},
		{Name: "unknown-policy", command: toolCommand{"unknown", "test", run}},
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
