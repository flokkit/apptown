package main

import (
	"strings"
	"testing"
)

func TestReleaseSetupRequiresItsOwnAcknowledgementAndPreservesImportedChoice(t *testing.T) {
	for _, save := range []bool{false, true} {
		seen := jsonAppTown(t, func(method, path string) (int, string) {
			switch path {
			case "/projects/shop/release/default":
				return 200, `{"revision":9,"legacySetup":{"targetId":"selected","deploymentName":"customer-test"}}`
			case "/projects/shop/targets":
				return 200, `{"targets":[{"id":"other","name":"other","displayLabel":"Other office","settingsRevision":5},{"id":"selected","name":"old","displayLabel":"Customer office","settingsRevision":0}]}`
			case "/projects/shop/release/default/setup":
				return 200, `{"deploymentId":"saved","revision":10}`
			default:
				t.Fatalf("unexpected request %s %s", method, path)
				return 500, `{}`
			}
		})
		args := []string{"release-setup", "shop"}
		if save {
			args = append(args, "--save")
		}
		out, err := run(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		if !save {
			if len(*seen) != 2 || !strings.Contains(out, "Customer office") || !strings.Contains(out, "customer-test") {
				t.Fatalf("preview changed setup or lost labels: %s %+v", out, *seen)
			}
			continue
		}
		if len(*seen) != 3 {
			t.Fatalf("setup invoked extra operation: %+v", *seen)
		}
		b := (*seen)[2].Body
		if b["targetId"] != "selected" || b["targetRevision"] != float64(0) || b["expectedRevision"] != float64(9) || b["deploymentName"] != "customer-test" || b["requirePromotion"] != true || !strings.Contains(out, "No release has started") {
			t.Fatalf("setup lost admitted candidate: %s %+v", out, b)
		}
	}
}

func TestReleaseSetupDoesNotRefillClearedOrUnavailableChoices(t *testing.T) {
	for _, preference := range []string{`{"revision":4}`, `{"revision":4,"legacySetup":{"targetId":"gone","deploymentName":"old"}}`} {
		seen := jsonAppTown(t, func(_, path string) (int, string) {
			if path == "/projects/shop/release/default" {
				return 200, preference
			}
			return 200, `{"targets":[{"id":"available","name":"office","settingsRevision":3}]}`
		})
		if _, err := run(t, "release-setup", "shop", "--save"); err == nil {
			t.Fatal("saved setup without explicit available choice")
		}
		for _, r := range *seen {
			if r.Method != "GET" {
				t.Fatalf("inferred a replacement: %+v", *seen)
			}
		}
	}
}

func TestDefaultReleaseUsesCustomerSelectionAndBothAcknowledgements(t *testing.T) {
	seen := jsonAppTown(t, func(method, path string) (int, string) {
		if method == "GET" {
			return 200, `{"configured":true,"deployment":"customer","deploymentLabel":"Customer office","defaultRevision":7}`
		}
		return 403, `{"error":"deployment permission withdrawn"}`
	})
	out, err := run(t, "release", "shop", "--accept-uncopied-data", "--accept-unchecked-copy", "--json")
	if err == nil || !strings.Contains(err.Error(), "permission withdrawn") || out != "" || len(*seen) != 2 {
		t.Fatalf("release refused before owner or invented success: %q %v %+v", out, err, *seen)
	}
	b := (*seen)[1].Body
	if (*seen)[1].Path != "/projects/shop/release" || b["defaultRevision"] != float64(7) || b["acceptUncopiedData"] != true || b["acceptUncheckedCopy"] != true {
		t.Fatalf("release dropped displayed identity or acknowledgements: %+v", *seen)
	}
}

func TestDefaultReleaseNeverPerformsMissingSetup(t *testing.T) {
	seen := jsonAppTown(t, func(_, _ string) (int, string) {
		return 200, `{"configured":false,"configError":"preference deliberately cleared","defaultRevision":9}`
	})
	if _, err := run(t, "release", "shop", "--json"); err == nil || !strings.Contains(err.Error(), "release-setup") {
		t.Fatalf("missing setup: %v", err)
	}
	if len(*seen) != 1 || (*seen)[0].Method != "GET" {
		t.Fatalf("release configured a preference: %+v", *seen)
	}
}

func TestReleaseSetupKeepsConfigurationRefusalSeparate(t *testing.T) {
	seen := jsonAppTown(t, func(method, path string) (int, string) {
		if method == "POST" {
			return 409, `{"error":"environment settings moved"}`
		}
		if path == "/projects/shop/release/default" {
			return 200, `{"revision":2}`
		}
		return 200, `{"targets":[{"id":"selected","displayLabel":"Customer office","settingsRevision":8}]}`
	})
	out, err := run(t, "release-setup", "shop", "--target", "selected", "--name", "customer", "--save")
	if err == nil || !strings.Contains(err.Error(), "environment settings moved") || strings.Contains(out, "saved") || len(*seen) != 3 {
		t.Fatalf("setup refusal became release/success: %q %v %+v", out, err, *seen)
	}
	if (*seen)[2].Body["targetRevision"] != float64(8) || (*seen)[2].Body["expectedRevision"] != float64(2) {
		t.Fatalf("setup not revision-bound: %+v", *seen)
	}
}
