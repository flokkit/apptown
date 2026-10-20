package workflowexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/execfile"

	"flokkit.io/forge/internal/execution"
)

// Detect supported project test commands, excluding npm's placeholder.
func TestDetectTestCommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got := detectTestCommand(dir); got != "" {
		t.Fatalf("empty dir must detect nothing, got %q", got)
	}
	_ = os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`), 0o644)
	if got := detectTestCommand(dir); got != "" {
		t.Fatalf("npm's no-test placeholder must not count, got %q", got)
	}
	_ = os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"scripts":{"test":"vitest run"}}`), 0o644)
	if got := detectTestCommand(dir); !strings.Contains(got, "npm test") {
		t.Fatalf("real npm test script must detect, got %q", got)
	}
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644)
	// Go writes its own report format to the file the step reads.
	if got := detectTestCommand(dir); got != `go test -json ./... > "$APPTOWN_TEST_REPORT"` {
		t.Fatalf("go.mod must win, got %q", got)
	}
	mvn := t.TempDir()
	_ = os.WriteFile(filepath.Join(mvn, "pom.xml"), []byte("<project/>"), 0o644)
	if got := detectTestCommand(mvn); !strings.Contains(got, "mvn") {
		t.Fatalf("pom.xml must detect maven, got %q", got)
	}
}

// A tests run whose declared toolchain did not install, and whose command
// then failed, is the forge's error (the tests never had their tools), not
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
			if err := execfile.Write(filepath.Join(bin, "mise"), []byte("#!/bin/sh\n"+tc.mise+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
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
