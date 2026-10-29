package main

import (
	"strings"
	"testing"
)

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

func TestDefaultReleaseWithoutAPreferenceNamesTheWayForward(t *testing.T) {
	seen := jsonAppTown(t, func(_, _ string) (int, string) {
		return 200, `{"configured":false,"configError":"preference deliberately cleared","defaultRevision":9}`
	})
	if _, err := run(t, "release", "shop", "--json"); err == nil || !strings.Contains(err.Error(), "apptown release shop <deployment>") {
		t.Fatalf("missing default: %v", err)
	}
	if len(*seen) != 1 || (*seen)[0].Method != "GET" {
		t.Fatalf("release configured a preference: %+v", *seen)
	}
}
