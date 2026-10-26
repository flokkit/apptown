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

func TestJanitorAssessmentNamesOnlyTheRuleAndRun(t *testing.T) {
	seen := jsonAppTown(t, func(method, path string) (int, string) {
		if method != "POST" || path != "/standing-approvals/rule/assess" {
			t.Fatalf("unexpected assessment effect %s %s", method, path)
		}
		return 200, `{"processingAvailable":false,"inspection":{"classification":"routine-dependency"}}`
	})
	if _, err := run(t, "janitor", "assess", "rule"); err == nil || len(*seen) != 0 {
		t.Fatalf("missing run admitted: %v", err)
	}
	out, err := run(t, "janitor", "assess", "rule", "--run", "exact-run")
	if err != nil || len(*seen) != 1 {
		t.Fatalf("assessment %q %v", out, err)
	}
	if len((*seen)[0].Body) != 1 || (*seen)[0].Body["runId"] != "exact-run" || !strings.Contains(out, `"processingAvailable": false`) {
		t.Fatalf("unexpected evidence or approval claim: %q %+v", out, *seen)
	}
}

func TestJanitorPublicationNeedsExactMeaningAndStableHumanRequest(t *testing.T) {
	file := filepath.Join(t.TempDir(), "transition.json")
	if err := os.WriteFile(file, []byte(`{"kind":"block","id":"00000000-0001-7000-8000-000000000000","from":1,"to":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	seen := jsonAppTown(t, func(method, path string) (int, string) {
		if method != "POST" || !strings.HasPrefix(path, "/routine-publications") {
			t.Fatalf("unexpected effect %s %s", method, path)
		}
		return 200, `{"digest":"exact-material","before":{"body":"old"},"after":{"body":"new"},"revocation":null}`
	})
	for _, extra := range [][]string{nil, {"--confirm", "exact-material"}, {"--request", "stable-act"}} {
		args := append([]string{"janitor", "publication", "classify", "--file", file}, extra...)
		if _, err := run(t, args...); err == nil || len(*seen) != 0 {
			t.Fatalf("implicit act accepted: %v %+v", err, *seen)
		}
	}
	out, err := run(t, "janitor", "publication", "preview", "--file", file)
	if err != nil || !strings.Contains(out, `"old"`) || !strings.Contains(out, `"new"`) {
		t.Fatalf("preview omitted exact material: %q %v", out, err)
	}
	for range 2 {
		if _, err = run(t, "janitor", "publication", "classify", "--file", file, "--confirm", "exact-material", "--request", "stable-act"); err != nil {
			t.Fatal(err)
		}
	}
	if len(*seen) != 3 || (*seen)[1].Body["shownDigest"] != "exact-material" || (*seen)[1].Body["requestId"] != "stable-act" || (*seen)[2].Body["requestId"] != "stable-act" {
		t.Fatalf("request retry changed meaning: %+v", *seen)
	}
}
