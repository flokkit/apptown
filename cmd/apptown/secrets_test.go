package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `apptown secret set NAME` reads the value from standard input and sends it
// in the body of PUT /secrets/NAME, never in the path or on the command
// line; a value of several lines is refused before anything is sent.
//
// Kills: the value taken from argv; the trailing newline of `echo` kept as
// part of the value; a multi-line value sent.
func TestSecretSetReadsTheValueFromStandardInput(t *testing.T) {
	seen := fakeForge(t, func(string, string) (int, string) { return 200, `{"name":"DO_TOKEN","held":true}` })
	feed := func(content string) {
		path := filepath.Join(t.TempDir(), "value")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		prev := secretIn
		secretIn = f
		t.Cleanup(func() { secretIn = prev })
	}
	feed("dop_v1_from-stdin\n")
	out, err := run(t, "secret", "set", "DO_TOKEN")
	if err != nil || strings.Contains(out, "dop_v1_from-stdin") {
		t.Fatalf("set: %q %v", out, err)
	}
	if len(*seen) != 1 || (*seen)[0].Method != "PUT" || (*seen)[0].Path != "/secrets/DO_TOKEN" || (*seen)[0].Body["value"] != "dop_v1_from-stdin" {
		t.Fatalf("the request: %+v", *seen)
	}
	feed("line one\nline two\n")
	if _, err := run(t, "secret", "set", "DO_TOKEN"); err == nil || !strings.Contains(err.Error(), "several lines") {
		t.Fatalf("a multi-line value: %v", err)
	}
	if _, err := run(t, "secret", "set", "DO_TOKEN", "dop_v1_on-argv"); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("a value on the command line: %v", err)
	}
	if _, err := run(t, "secret", "remove", "DO_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 2 || (*seen)[1].Method != "DELETE" || (*seen)[1].Path != "/secrets/DO_TOKEN" {
		t.Fatalf("the requests: %+v", *seen)
	}
}
