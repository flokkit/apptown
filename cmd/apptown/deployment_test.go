package main

import (
	"strings"
	"testing"
)

func TestConfirmFeedUsesDisplayedRevision(t *testing.T) {
	seen := jsonAppTown(t, func(method, path string) (int, string) {
		if method == "GET" {
			return 200, `[{"name":"staff","feedRevision":7}]`
		}
		return 200, `{"feedConfirmed":true}`
	})
	out, err := run(t, "deployment", "confirm-feed", "shop", "staff")
	if err != nil || !strings.Contains(out, "Confirmed follows main: shop/staff") || len(*seen) != 2 {
		t.Fatalf("confirm: %s %v %+v", out, err, *seen)
	}
	if (*seen)[1].Path != "/projects/shop/deployments/staff/confirm-feed" || (*seen)[1].Body["revision"] != float64(7) {
		t.Fatalf("confirmation lost revision: %+v", *seen)
	}
}
func TestConfirmAllFeedsReportsRefusals(t *testing.T) {
	seen := jsonAppTown(t, func(method, path string) (int, string) {
		return 200, `{"confirmed":["shop/staff"],"refused":["shop/public: release permission required"]}`
	})
	out, err := run(t, "deployment", "confirm-feed", "--all")
	if err == nil || len(*seen) != 1 || (*seen)[0].Path != "/deployments/confirm-feed" || !strings.Contains(out, "Confirmed follows main: shop/staff") || !strings.Contains(out, "Not confirmed:") {
		t.Fatalf("bulk confirm: %s %v %+v", out, err, *seen)
	}
}

func TestSetFeedUsesDisplayedRevisionAndNamedSource(t *testing.T) {
	seen := jsonAppTown(t, func(method, path string) (int, string) {
		if method == "GET" {
			return 200, `[{"name":"production","feedRevision":7}]`
		}
		return 200, `{}`
	})
	_, err := run(t, "deployment", "set-feed", "shop", "production", "promotion", "--from", "staging")
	if err != nil || len(*seen) != 2 {
		t.Fatalf("set-feed: %v %+v", err, *seen)
	}
	got := (*seen)[1]
	if got.Method != "PUT" || got.Path != "/projects/shop/deployments/production/feed" || got.Body["revision"] != float64(7) || got.Body["feed"] != "promotion" || got.Body["from"] != "staging" {
		t.Fatalf("settings write: %+v", got)
	}
}

func TestSetFeedRefusesMissingOrUnrelatedSourceBeforeWriting(t *testing.T) {
	for _, args := range [][]string{{"promotion"}, {"main", "--from", "staging"}, {"unknown"}} {
		seen := jsonAppTown(t, func(_, _ string) (int, string) { return 200, `{}` })
		if _, err := run(t, append([]string{"deployment", "set-feed", "shop", "production"}, args...)...); err == nil || len(*seen) != 0 {
			t.Fatalf("invalid feed reached API: %v %+v", err, *seen)
		}
	}
}

func TestFeedEditsKeepOrExplicitlyChangeTheApprovalRule(t *testing.T) {
	for _, role := range []string{"", "none", "project-approver"} {
		t.Run("role="+role, func(t *testing.T) {
			seen := jsonAppTown(t, func(method, path string) (int, string) {
				if method == "GET" {
					return 200, `[{"name":"production","feedRevision":7,"approvalRole":"release-manager"}]`
				}
				return 200, `{}`
			})
			args := []string{"deployment", "set-feed", "shop", "production", "manual"}
			if role != "" {
				args = append(args, "--approval-role", role)
			}
			if _, err := run(t, args...); err != nil {
				t.Fatal(err)
			}
			want := role
			if role == "" {
				want = "release-manager"
			}
			if role == "none" {
				want = ""
			}
			if len(*seen) != 2 || (*seen)[1].Body["approvalRole"] != want {
				t.Fatalf("settings lost approval rule: %+v", *seen)
			}
		})
	}
}
