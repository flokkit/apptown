package execution_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// A check image is built only through the build it is given (the one
// build rule's, dockerbuild), never by a `docker build` of its own; and a
// process run on this machine itself is not given the containers'
// certificate script, which run there would change this machine's store.
// Not parallel: it installs the process's trust.
func TestBuildsGoThroughTheBuildRuleAndLocalProcessesGetNoCertificates(t *testing.T) {
	trusttest.Install(t, trusttest.New(t, "Company CA").Bundle(t), trust.Bundle{})
	tree := t.TempDir()
	if err := os.Mkdir(filepath.Join(tree, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	var build []string
	execution.PrepareImage(context.Background(), func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
		if args[0] == "build" {
			t.Errorf("the check image was built by a docker build of its own: %q", args)
		}
		return nil, errors.New("Error: No such image")
	}, func(_ context.Context, args ...string) ([]byte, error) {
		build = args
		return nil, nil
	}, execution.ImageRequest{Tag: "apptown-conform:abc", Tree: tree, Context: "app", AllowBuild: true})
	if want := []string{"--label", execution.CheckLabel, "-t", "apptown-conform:abc", filepath.Join(tree, "app")}; !slices.Equal(build, want) {
		t.Fatalf("the check image's build: %q, want %q", build, want)
	}

	out := execution.Runner{}.Run(context.Background(), execution.Local, execution.Request{
		Workspace: t.TempDir(), Argv: []string{"sh", "-c", `echo "[$` + trust.EnvVar + `]"`}, Timeout: time.Minute,
	})
	if out.RunErr != nil || strings.TrimSpace(string(out.Output)) != "[]" {
		t.Fatalf("a local process inherited the containers' script: %q %v", out.Output, out.RunErr)
	}
	given := execution.Runner{}.Run(context.Background(), execution.Local, execution.Request{
		Workspace: t.TempDir(), Argv: []string{"sh", "-c", `echo "[$` + trust.EnvVar + `]"`}, Timeout: time.Minute,
		Env: []string{trust.EnvVar + "=named"},
	})
	if strings.TrimSpace(string(given.Output)) != "[named]" {
		t.Fatalf("a request that names the variable does not pass it: %q", given.Output)
	}
}

// Every container the runner starts is given the script by name, after the
// request's own environment; none with nothing configured.
// Not parallel: it installs the process's trust.
func TestEveryContainerIsGivenTheExtraCertificatesByName(t *testing.T) {
	req := execution.Request{Name: "c", Image: "img", Argv: []string{"true"}, Env: []string{"A=1"}}
	if args := execution.DockerArgs(req); slices.Contains(args, trust.EnvVar) {
		t.Fatalf("given a script with nothing configured: %q", args)
	}
	trusttest.Install(t, trusttest.New(t, "Company CA").Bundle(t), trust.Bundle{})
	args := execution.DockerArgs(req)
	at := slices.Index(args, trust.EnvVar)
	if at < 1 || args[at-1] != "-e" || at > slices.Index(args, "img") || slices.Index(args, "A=1") > at {
		t.Fatalf("container arguments %q", args)
	}
}
