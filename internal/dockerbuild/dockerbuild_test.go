package dockerbuild_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dockerbuild"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// A build on a machine's own daemon (a worker's, AppTown's) names the
// context's own builder, is given this machine's extra certificates as its
// secret and AppTown's label; a daemon without buildx or whose builder is
// not its own is refused before anything is built.
// Not parallel: it installs the process's trust.
func TestABuildFollowsTheOneRule(t *testing.T) {
	trusttest.Install(t, trusttest.New(t, "Company CA").Bundle(t), trust.Bundle{})
	daemon := func(driver string, buildx bool, built *[]string) dockerbuild.Daemon {
		return dockerbuild.Daemon{Local: true, Run: func(_ context.Context, args ...string) ([]byte, error) {
			switch strings.Join(args, " ") {
			case "buildx version":
				if !buildx {
					return []byte("docker: 'buildx' is not a docker command."), errors.New("exit status 1")
				}
				return []byte("github.com/docker/buildx v0.38.1"), nil
			case "context show":
				return []byte("worker\n"), nil
			case "buildx inspect worker":
				return []byte("Name: worker\nDriver: " + driver + "\n"), nil
			}
			if args[0] == "build" {
				*built = args
			}
			return nil, nil
		}}
	}
	var built []string
	if _, err := daemon("docker", true, &built).Build(context.Background(), "-t", "x", "ctx"); err != nil {
		t.Fatal(err)
	}
	line := strings.Join(built, " ")
	for _, want := range []string{"--builder worker -t x ctx", "--secret id=apptown-trust,env=APPTOWN_TRUST", strings.Join(dockerlabel.BuiltArgs(), " ")} {
		if !strings.Contains(line, want) {
			t.Errorf("the build %q lacks %q", line, want)
		}
	}
	for name, d := range map[string]dockerbuild.Daemon{"no buildx": daemon("", false, &built), "another builder": daemon("remote", true, &built)} {
		built = nil
		if _, err := d.Build(context.Background(), "-t", "x", "ctx"); err == nil || built != nil {
			t.Errorf("%s: %v, built %q", name, err, built)
		}
	}
}
