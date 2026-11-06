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
