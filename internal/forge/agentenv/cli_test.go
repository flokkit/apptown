package agentenv

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"flokkit.io/forge/internal/execfile"
)

func TestCLIExplicitArtifactIsIndependentOfLaterWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := execfile.Write("apptown-linux", []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	cli := NewCLI("apptown-linux")
	t.Chdir(t.TempDir())
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			path, err := cli.Path()
			if err != nil || path != filepath.Join(root, "apptown-linux") {
				t.Errorf("artifact: %q %v", path, err)
			}
		})
	}
	wg.Wait()
}

func TestCLIDefaultIsBesideExecutable(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cli := NewCLI("")
	if cli.err != nil || cli.path != filepath.Join(filepath.Dir(executable), "apptown-linux") {
		t.Fatalf("default: %+v", cli)
	}
}

func TestCLIUnavailableDoesNotBuildOrReturnAMount(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"missing", "directory", "not executable"} {
		t.Run(kind, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "apptown-linux")
			switch kind {
			case "directory":
				if err := os.Mkdir(p, 0755); err != nil {
					t.Fatal(err)
				}
			case "not executable":
				if err := os.WriteFile(p, []byte("fixture"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			cli := NewCLI(p)
			path, err := cli.Path()
			if err == nil || path != "" {
				t.Fatalf("unavailable artifact yielded mount: %q %v", path, err)
			}
			// Preserve the once-per-process result; an operator fixes packaging before
			// restart rather than initiating work during a later session request.
			if kind == "missing" {
				if err := execfile.Write(p, []byte("fixture"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			second, again := cli.Path()
			if second != "" || again != err {
				t.Fatalf("lookup repeated: %q %v", second, again)
			}
		})
	}
}

func TestCLIPathPrecedesToolchainWithoutChangingCallerEnvironment(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"", "/custom/bin:/usr/bin", CLIDir + ":/mise/shims:/usr/bin"} {
		input := []string{"FORGE_PROJECT=fixture"}
		if path != "" {
			input = append(input, "PATH="+path)
		}
		got := WithCLIPath(input)
		if len(input) > 1 && input[1] != "PATH="+path {
			t.Fatal("changed caller environment")
		}
		want := path
		if path == "" {
			want = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
		}
		if !strings.HasPrefix(want, CLIDir+":") {
			want = CLIDir + ":" + want
		}
		if got[len(got)-1] != "PATH="+want {
			t.Fatalf("CLI search path: %v", got)
		}
	}
}
