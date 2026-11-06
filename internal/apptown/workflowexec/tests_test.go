package workflowexec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execfile"

	"github.com/flokkit/apptown/internal/execution"
)

// A tests run whose declared toolchain did not install, and whose command
// then failed, is AppTown's error (the tests never had their tools), not
// the change's failure; one that passes anyway on the image's toolchains
// passes, and a failure with the toolchain installed is the tests'.
func TestAFailedToolchainInstallIsNotAFailedTest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, mise, command string
		exit                int
		unavailable         bool
	}{
		{"install failed, tests failed", "exit 1", "echo 'mvn: not found'; exit 127", toolchainUnavailableExit, true},
		{"install failed, tests passed on the image", "exit 1", "true", 0, false},
		{"installed, tests failed", "exit 0", "exit 3", 3, false},
		{"installed, tests passed", "exit 0", "true", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bin := t.TempDir()
			writeTestsMise(t, bin, "#!/bin/sh\n"+tc.mise+"\n")
			out := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{
				Workspace: t.TempDir(), Argv: []string{"sh", "-c", testsScript(tc.command)}, Timeout: time.Minute,
				Env: []string{"PATH=" + bin + ":/usr/bin:/bin", "MISE_DATA_DIR=" + t.TempDir()},
			})
			if !out.Answered() || out.ExitCode != tc.exit || toolchainUnavailable(out) != tc.unavailable {
				t.Fatalf("exit %d (want %d), toolchain unavailable %v: %+v\n%s", out.ExitCode, tc.exit, toolchainUnavailable(out), out, out.Output)
			}
		})
	}
}

// A command that writes no report leaves an empty one, which reads as "no
// report" wherever the tests ran; one it wrote is kept as written. (No mise
// here: its install fails, and a passing command still passes on the
// image's tools.)
func TestATestsScriptLeavesAnEmptyReportWhenTheCommandWroteNone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ command, want string }{
		{"true", ""},
		{`echo '{"Action":"pass"}' > "$APPTOWN_TEST_REPORT"`, `{"Action":"pass"}` + "\n"},
	} {
		report := filepath.Join(t.TempDir(), "report")
		out := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{
			Workspace: t.TempDir(), Argv: []string{"sh", "-c", testsScript(tc.command)}, Timeout: time.Minute,
			Env: []string{"PATH=/usr/bin:/bin", "MISE_DATA_DIR=" + t.TempDir(), testReportEnv + "=" + report},
		})
		got, err := os.ReadFile(report)
		if !out.Answered() || out.ExitCode != 0 || err != nil || string(got) != tc.want {
			t.Fatalf("%q: exit %d, report %q %v\n%s", tc.command, out.ExitCode, got, err, out.Output)
		}
	}
}

// Mise's installed/current query supplies versions, including on a failed test;
// an unreadable query must replace any snapshot committed with the source.
func TestTestsScriptSnapshotsInstalledVersionsBeforeTheCommand(t *testing.T) {
	for _, readable := range []bool{true, false} {
		t.Run(fmt.Sprint(readable), func(t *testing.T) {
			dir, bin := t.TempDir(), t.TempDir()
			snapshot := filepath.Join(dir, execution.ToolchainSnapshot)
			if err := os.WriteFile(snapshot, []byte(`{"stale":[]}`), 0600); err != nil {
				t.Fatal(err)
			}
			query := `echo '{"go":[{"version":"1.25.4"}]}'`
			if !readable {
				query = "exit 1"
			}
			script := "#!/bin/sh\ncase \"$1\" in ls) [ \"$*\" = 'ls --current --installed --json' ] || exit 2; " + query + ";; *) exit 0;; esac\n"
			writeTestsMise(t, bin, script)
			out := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{
				Workspace: dir, Argv: []string{"sh", "-c", testsScript("exit 3")}, Timeout: time.Minute,
				Env: []string{"PATH=" + bin + ":/usr/bin:/bin", "MISE_DATA_DIR=" + bin},
			})
			raw, err := os.ReadFile(snapshot)
			if !out.Answered() || out.ExitCode != 3 {
				t.Fatalf("test verdict changed: %+v", out)
			}
			if readable && (err != nil || !strings.Contains(string(raw), "1.25.4")) {
				t.Fatalf("resolved versions missing: %s %v", raw, err)
			}
			if !readable && !os.IsNotExist(err) {
				t.Fatalf("unreadable query kept stale versions: %s %v", raw, err)
			}
		})
	}
}

// These tests exercise the actual install/query shell protocol and exit status.
func writeTestsMise(t *testing.T, bin, script string) {
	t.Helper()
	if err := execfile.Write(filepath.Join(bin, "mise"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}
