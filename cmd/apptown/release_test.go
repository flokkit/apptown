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

// The CLI deliberately sends no displayed-release expectation; the owner's
// selected source release is printed from its persisted log.
func TestNamedReleaseSelectsAtRequestTimeAndPrintsPromotedSource(t *testing.T) {
	seen := jsonAppTown(t, func(method, path string) (int, string) {
		if method == "POST" {
			return 202, `{"number":19,"state":"releasing"}`
		}
		return 200, `{"release":{"state":"released"},"events":[{"text":"promoted release 18 from staging"}]}`
	})
	out, err := run(t, "release", "shop", "production")
	if err != nil || !strings.Contains(out, "promoted release 18 from staging") {
		t.Fatalf("promotion log: %q %v", out, err)
	}
	if len(*seen) != 2 || (*seen)[0].Path != "/projects/shop/deployments/production/deploy" || len((*seen)[0].Body) != 0 {
		t.Fatalf("request-time selection: %+v", *seen)
	}
}

func TestReleaseApprovalDisplaysAndSendsTheExactMeaning(t *testing.T) {
	for _, yes := range []bool{false, true} {
		t.Run(map[bool]string{false: "question", true: "assent"}[yes], func(t *testing.T) {
			seen := jsonAppTown(t, func(method, path string) (int, string) {
				if method == "GET" {
					return 200, `{"decision":{"meaning":"I accept release 7 running on production","canApprove":true}}`
				}
				return 202, `{"ok":true}`
			})
			args := []string{"release", "approve", "shop", "7"}
			if yes {
				args = append(args, "-y")
			}
			out, err := run(t, args...)
			if !strings.Contains(out, "I accept release 7 running on production") {
				t.Fatalf("meaning not shown: %q %v", out, err)
			}
			if yes {
				if err != nil || len(*seen) != 2 || (*seen)[1].Path != "/projects/shop/releases/7/approve" || (*seen)[1].Body["meaning"] != "I accept release 7 running on production" {
					t.Fatalf("assent: %v %+v", err, *seen)
				}
			} else if err == nil || len(*seen) != 1 {
				t.Fatalf("approval sent without acceptance: %v %+v", err, *seen)
			}
		})
	}
}

func TestReleaseCancelAndWaitingRequestDoNotFollowADeploy(t *testing.T) {
	seen := jsonAppTown(t, func(method, path string) (int, string) { return 202, `{"number":7,"state":"waiting for approval"}` })
	if _, err := run(t, "release", "cancel", "shop", "7"); err != nil || len(*seen) != 1 || (*seen)[0].Path != "/projects/shop/releases/7/cancel" {
		t.Fatalf("cancel: %v %+v", err, *seen)
	}
	*seen = nil
	out, err := run(t, "release", "shop", "production")
	if err != nil || len(*seen) != 1 || !strings.Contains(out, "waiting for approval") {
		t.Fatalf("waiting request: %q %v %+v", out, err, *seen)
	}
}
