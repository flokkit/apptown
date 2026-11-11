package awsecs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
)

// Run the generated shell against an endpoint that accepts partial bytes too.
// A fake task exit alone would not prove the exporter/upload ordering.
func TestCopyRequiresCleanExportBeforeAnyUpload(t *testing.T) {
	var puts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Errorf("%s", r.Method)
		}
		puts.Add(1)
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(200)
	}))
	defer server.Close()
	// The runner's fixed scratch path is moved into this test's private folder;
	// package installation is separate from the shell boundary under test.
	for _, tt := range []struct {
		name, command      string
		wantExit, wantPuts int
	}{{"failed exporter", "printf partial; exit 7", 7, 0}, {"successful exporter", "printf complete", 0, 1}} {
		t.Run(tt.name, func(t *testing.T) {
			puts.Store(0)
			script := copyScript(tt.command, true)
			script = strings.Replace(script, "apk add --no-cache curl >/dev/null\n", "", 1)
			script = strings.ReplaceAll(script, "/tmp/apptown-copy", t.TempDir()+"/copy")
			out := runShell(t, script, server.URL)
			if out.ExitCode != tt.wantExit || int(puts.Load()) != tt.wantPuts {
				t.Fatalf("exit %d; PUTs %d; %s", out.ExitCode, puts.Load(), out.Output)
			}
		})
	}
}

// A count failure or a table added after listing must fail before uploading
// empty evidence. This exercises the task's shell rather than a claimed exit.
func TestEmptyDatabaseIsCheckedAgainBeforeItsProofUploads(t *testing.T) {
	for _, count := range []string{"0", "1", "failed"} {
		t.Run(count, func(t *testing.T) {
			var puts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				puts.Add(1)
				body, _ := io.ReadAll(r.Body)
				if string(body) != "empty\n" {
					t.Errorf("proof: %q", body)
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			fakePSQL := "psql() { printf '%s\\n' '" + count + "'; };\n"
			if count == "failed" {
				fakePSQL = "psql() { return 7; };\n"
			}

			script := strings.Replace(copyScript(checkEmptyCommand(), true), "apk add --no-cache curl >/dev/null\n", "", 1)
			script = strings.ReplaceAll(script, "/tmp/apptown-copy", filepath.Join(dir, "copy"))
			out := runShell(t, fakePSQL+script, server.URL)
			if count == "0" {
				if out.ExitCode != 0 || puts.Load() != 1 {
					t.Fatalf("empty postgres: exit %d %s, puts %d", out.ExitCode, out.Output, puts.Load())
				}
			} else if out.ExitCode == 0 || puts.Load() != 0 {
				t.Fatalf("not shown empty: exit %d %s, puts %d", out.ExitCode, out.Output, puts.Load())
			}
		})
	}
}

// runShell runs a generated job script on this machine through the owner of
// process starts, with the upload URL the job would be given.
func runShell(t *testing.T, script, url string) execution.Outcome {
	t.Helper()
	out := (execution.Runner{}).Run(t.Context(), execution.Local, execution.Request{Argv: []string{"/bin/sh", "-c", script}, Env: []string{"APPTOWN_COPY_URL=" + url}, Timeout: time.Minute})
	if out.RunErr != nil && out.ExitCode < 0 {
		t.Fatalf("shell did not run: %v", out.RunErr)
	}
	return out
}

func TestInstallationNamesAreDisjointAndHashExactBytes(t *testing.T) {
	if installationPart("abc") != "ba7816bf" {
		t.Fatal("prefix differs from setup command")
	}
	if installationPart("abc") == installationPart("abc\n") {
		t.Fatal("prefix trimmed the installation id")
	}
}
