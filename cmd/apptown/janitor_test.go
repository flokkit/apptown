package main

import (
	"strings"
	"testing"
)

func TestJanitorSetSendsChoicesAndExpectedRevision(t *testing.T) {
	seen := jsonAppTown(t, func(method, path string) (int, string) {
		if method != "PUT" || path != "/janitor/choices" {
			t.Fatalf("%s %s", method, path)
		}
		return 200, `{}`
	})
	if _, err := run(t, "janitor", "set", "--dependencies", "aggressive", "--security-fixes", "--base-images", "--toolchain-patches", "--packs", "--revision", "3"); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 || (*seen)[0].Body["expectedRevision"] != float64(3) {
		t.Fatal(*seen)
	}
	choices := (*seen)[0].Body["choices"].(map[string]any)
	if choices["dependencies"] != "aggressive" || choices["packs"] != true || choices["baseImages"] != true || choices["toolchainPatches"] != true || choices["securityFixes"] != true {
		t.Fatal(choices)
	}
	if _, err := run(t, "janitor", "set", "--dependencies", "off"); err == nil || len(*seen) != 1 {
		t.Fatalf("revision inferred: %v", err)
	}
}
func TestJanitorReadsUseTheShippedContract(t *testing.T) {
	seen := jsonAppTown(t, func(method, path string) (int, string) {
		if method != "GET" {
			t.Fatal(method)
		}
		return 200, `[]`
	})
	for _, verb := range []string{"choices", "history"} {
		if _, err := run(t, "janitor", verb); err != nil {
			t.Fatal(err)
		}
	}
	if len(*seen) != 2 || (*seen)[0].Path != "/janitor/choices" || (*seen)[1].Path != "/janitor/choices/history" {
		t.Fatal(*seen)
	}
}
func TestJanitorAssessmentNamesOnlyTheRun(t *testing.T) {
	seen := jsonAppTown(t, func(method, path string) (int, string) {
		if method != "POST" || path != "/janitor/assess" {
			t.Fatalf("%s %s", method, path)
		}
		return 200, `{}`
	})
	if _, err := run(t, "janitor", "assess", "--run", "exact-run"); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 || len((*seen)[0].Body) != 1 || (*seen)[0].Body["run"] != "exact-run" {
		t.Fatal(*seen)
	}
}
func TestJanitorAttemptsReadsTheNamedProjectAndQueue(t *testing.T) {
	seen := jsonAppTown(t, func(method, path string) (int, string) {
		if method != "GET" || path != "/projects/my-app/janitor/attempts" {
			t.Fatalf("unexpected request: %s %s", method, path)
		}
		return 200, `[]`
	})
	if _, err := run(t, "janitor", "attempts", "-p", "my-app", "--queue", "security"); err != nil || len(*seen) != 1 {
		t.Fatalf("attempt read: %v %+v", err, seen)
	}
	if _, err := run(t, "janitor", "attempts"); err == nil || len(*seen) != 1 {
		t.Fatalf("missing project made a request: %v %+v", err, seen)
	}
}

func TestJanitorRunReadsExpectedChangeAndPrintsEachStart(t *testing.T) {
	seen := jsonAppTown(t, func(method, path string) (int, string) {
		if method == "GET" && path == "/projects/app/scans" {
			return 200, `{"queues":[{"queue":"updates","changeId":"00000000-0001-7000-8000-000000000000"}]}`
		}
		if method == "POST" && path == "/projects/app/janitor/run" {
			return 200, `{"starts":[{"queue":"updates","refused":"Already running: change #9"},{"queue":"packs","current":true}]}`
		}
		t.Fatalf("unexpected request: %s %s", method, path)
		return 500, ""
	})
	out, err := run(t, "janitor", "run", "-p", "app", "-queue", "updates", "-again")
	if err != nil || !strings.Contains(out, "Already running: change #9") || !strings.Contains(out, `"current": true`) {
		t.Fatalf("starts: %q %v", out, err)
	}
	if len(*seen) != 2 || (*seen)[1].Body["changeId"] != "00000000-0001-7000-8000-000000000000" || (*seen)[1].Body["again"] != true || (*seen)[1].Body["queue"] != "updates" {
		t.Fatalf("requests: %+v", *seen)
	}
	if _, err := run(t, "janitor", "run", "-p", "app", "-again"); err == nil || len(*seen) != 2 {
		t.Fatalf("unnamed retry: %v %+v", err, *seen)
	}
}
