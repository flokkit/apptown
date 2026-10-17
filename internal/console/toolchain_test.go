package console

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/execfile"
)

// A declared toolchain that does not install is said, not hidden: on
// stderr with the installer's own words, and to the agent after its
// prompt. One that installs changes nothing (a session whose Ruby never
// installed hand-wrote a Rails skeleton, and nobody saw why).
func TestAFailedToolchainInstallIsSaidToTheLogAndTheAgent(t *testing.T) {
	t.Parallel()
	// One container's scratch across both sessions: a failure's mark must
	// not tell the next session about an install that worked.
	scratch := t.TempDir()
	for _, fails := range []bool{true, false} {
		work, bin := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(work, "mise.toml"), []byte("[tools]\nruby = \"3.3\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		install := "exit 0"
		if fails {
			install = `echo "ruby@3.3.0: no precompiled ruby for this platform" >&2; exit 1`
		}
		mise := "#!/bin/sh\ncase \"$1\" in install) " + install + ";; esac\nexit 0\n"
		if err := execfile.Write(filepath.Join(bin, "mise"), []byte(mise), 0o755); err != nil {
			t.Fatal(err)
		}
		argv := toolchainArgs(work, scratch, []string{"sh", "-c", `printf '%s|%s' "$1" "$2"`, "sh", "--flag", "Implement the change."})
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("fails=%v: %v %s", fails, err, stderr.String())
		}
		got := string(out)
		if !strings.HasPrefix(got, "--flag|Implement the change.") {
			t.Fatalf("fails=%v: the command's words changed: %q", fails, got)
		}
		said := strings.Contains(stderr.String(), "did not install") && strings.Contains(stderr.String(), "no precompiled ruby")
		told := strings.HasSuffix(got, toolchainInstallNote(scratch))
		if said != fails || told != fails {
			t.Fatalf("fails=%v: said %v, told the agent %v\nstderr: %s\nargv: %q", fails, said, told, stderr.String(), got)
		}
	}
}
