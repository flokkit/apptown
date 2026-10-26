package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJanitorRequiresExplicitShownConfirmationAndDoesNotRelease(t *testing.T) {
	file := filepath.Join(t.TempDir(), "consent.json")
	if err := os.WriteFile(file, []byte(`{"delivery":"full","projects":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	seen := jsonAppTown(t, func(method, path string) (int, string) {
		if method != "POST" || !strings.HasPrefix(path, "/standing-approvals") {
			t.Fatalf("unexpected effect: %s %s", method, path)
		}
		return 200, `{"digest":"exact-shown","shown":"bounded delivery consent"}`
	})
	if _, err := run(t, "janitor", "enable", "--file", file); err == nil || len(*seen) != 0 {
		t.Fatalf("enable inferred confirmation: %v %+v", err, *seen)
	}
	if _, err := run(t, "janitor", "preview", "--file", file); err != nil || len(*seen) != 1 || (*seen)[0].Path != "/standing-approvals/preview" {
		t.Fatalf("preview: %v %+v", err, *seen)
	}
	if _, err := run(t, "janitor", "enable", "--file", file, "--confirm", "exact-shown"); err != nil || len(*seen) != 2 {
		t.Fatalf("explicit enable: %v %+v", err, *seen)
	}
	if (*seen)[1].Body["shownDigest"] != "exact-shown" || (*seen)[1].Body["expectedRevision"] != float64(0) {
		t.Fatalf("lost confirmation/revision: %+v", (*seen)[1])
	}
}
func TestJanitorRefusalNeverPrintsSavedConsent(t *testing.T) {
	seen := jsonAppTown(t, func(_, _ string) (int, string) { return 409, `{"error":"standing approval changed"}` })
	out, err := run(t, "janitor", "disable", "rule-id", "--revision", "2")
	if err == nil || !strings.Contains(err.Error(), "changed") || out != "" || len(*seen) != 1 {
		t.Fatalf("false successful disable: %q %v %+v", out, err, *seen)
	}
}

func TestJanitorReadsUseTheShippedContract(t *testing.T) {
	seen := jsonAppTown(t, func(method, path string) (int, string) {
		if method != "GET" {
			t.Fatalf("read caused effect: %s %s", method, path)
		}
		return 200, `[]`
	})
	for _, args := range [][]string{{"janitor", "list", "--project", "00000000-0001-7000-8000-000000000000"}, {"janitor", "history", "00000000-0002-7000-8000-000000000000"}} {
		if _, err := run(t, args...); err != nil {
			t.Fatal(err)
		}
	}
	if len(*seen) != 2 {
		t.Fatalf("reads: %+v", seen)
	}
}
