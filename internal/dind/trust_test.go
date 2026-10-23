package dind_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dind"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// A private daemon is started through the extra certificates: its
// container is given the script by name and runs it before the image's own
// entrypoint, with the daemon's flags passed on.
// Not parallel: it installs the process's trust.
func TestAPrivateDaemonTrustsTheExtraCertificatesBeforeItStarts(t *testing.T) {
	trusttest.Install(t, trusttest.New(t, "Company CA").Bundle(t), trust.Bundle{})
	var run []string
	err := dind.Daemon{Name: "d", Network: "n", Image: "docker:27-dind", OwnerLabel: "o", Owner: "x", Options: []string{"--dns=1.1.1.1"}}.
		Create(context.Background(), func(_ context.Context, args ...string) ([]byte, error) { run = args; return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	image := slices.Index(run, "docker:27-dind")
	want := []string{"docker:27-dind", "-c", trust.Run + `; exec dockerd-entrypoint.sh "$@"`, "sh", "--tls=false", "--dns=1.1.1.1"}
	if image < 0 || !slices.Equal(run[image:], want) || !strings.Contains(strings.Join(run[:image], " "), "-e "+trust.EnvVar+" --entrypoint sh") {
		t.Fatalf("docker %q", run)
	}
}

// The builder a private daemon runs builds in is given the extra
// certificates once, when it is made: it is started, the script is run in
// its container as root, and it is restarted to read them. With none
// configured it is only made.
// Not parallel: it installs the process's trust.
func TestThePrivateBuilderIsGivenTheExtraCertificatesWhenMade(t *testing.T) {
	builder := func() []string {
		var calls []string
		err := dind.Builder(context.Background(), func(_ context.Context, args ...string) ([]byte, error) {
			calls = append(calls, strings.Join(args, " "))
			if slices.Contains(args, "inspect") && !slices.Contains(args, "--bootstrap") {
				return nil, context.Canceled // no builder yet
			}
			return nil, nil
		}, "dind-1", "/client", "/forge-dind/buildkitd.toml")
		if err != nil {
			t.Fatal(err)
		}
		return calls
	}
	if calls := builder(); len(calls) != 2 {
		t.Fatalf("with nothing configured: %q", calls)
	}
	trusttest.Install(t, trusttest.New(t, "Company CA").Bundle(t), trust.Bundle{})
	calls := builder()
	if len(calls) != 5 ||
		!strings.HasSuffix(calls[2], "docker buildx inspect --bootstrap apptown") ||
		!strings.HasSuffix(calls[3], "docker exec -e "+trust.EnvVar+" "+dind.BuilderContainer+" sh -c "+trust.Run) ||
		!strings.HasSuffix(calls[4], "docker restart "+dind.BuilderContainer) {
		t.Fatalf("builder calls:\n%s", strings.Join(calls, "\n"))
	}
}
