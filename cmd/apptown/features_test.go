package main

import (
	"strings"
	"testing"
)

func TestFeaturesCLIUsesTheSharedAPIAndProtectsItsRead(t *testing.T) {
	seen := jsonAppTown(t, func(method, path string) (int, string) {
		if method == "GET" && path == "/features" {
			return 200, `[{"area":"evals","name":"Evals","tier":"beta","enabled":false,"covers":"Evaluations"}]`
		}
		return 200, `{"area":"evals","enabled":true}`
	})
	out, err := run(t, "features")
	if err != nil || !strings.Contains(out, "beta") || !strings.Contains(out, "off") {
		t.Fatalf("list: %q %v", out, err)
	}
	if _, err := run(t, "features", "on", "evals"); err != nil {
		t.Fatal(err)
	}
	last := (*seen)[len(*seen)-1]
	if last.Method != "PUT" || last.Path != "/features/evals" || last.Body["expected"] != false || last.Body["enabled"] != true {
		t.Fatalf("different switch door: %+v", last)
	}
}
