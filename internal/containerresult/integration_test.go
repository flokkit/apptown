//go:build integration

package containerresult_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/containerresult"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// busybox is the tool versions' df image, which the heavy tiers pull.
const busybox = "busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"

func cli(ctx context.Context, env []string, args ...string) ([]byte, error) {
	return docker.CLI{}.Run(ctx, "", docker.Command{Args: args, Env: env})
}

// A container whose stdout Docker keeps none of (no log driver) still
// answers: the answer travels as a file, never through the stream that was
// lost in CI.
func TestIntegrationAResultArrivesThoughDockerKeepsNoneOfItsOutput(t *testing.T) {
	t.Parallel()
	got, err := containerresult.Run(context.Background(), cli, containerresult.Spec{Image: busybox,
		Options: []string{"--network", "none", "--log-driver", "none"}, Script: `echo "uid $(id -u)"; echo "$WORD"`, Env: []string{"WORD=carried"}})
	if err != nil || string(got) != "uid 0\ncarried\n" {
		t.Fatalf("got %q, %v; want the answer from its file", got, err)
	}
	var exit *containerresult.ExitError
	if _, err := containerresult.Run(context.Background(), cli, containerresult.Spec{Image: busybox, Script: "echo half; exit 4"}); !errors.As(err, &exit) || exit.Code != 4 {
		t.Fatalf("got %v; want exit status 4 and no answer", err)
	}
}

// RunIn answers through a file in a running container, and leaves nothing
// behind there.
func TestIntegrationRunInAnswersThroughAFileAndRemovesIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	out, err := cli(ctx, nil, "run", "-d", "--rm", "--network", "none", "--label", "apptown.class=test", busybox, "sleep", "120")
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _, _ = cli(context.Background(), nil, "rm", "-f", id) })
	got, err := containerresult.RunIn(ctx, cli, id, containerresult.Spec{Script: "echo rows 12; echo tables 3"})
	if err != nil || string(got) != "rows 12\ntables 3\n" {
		t.Fatalf("got %q, %v; want the measures from the file", got, err)
	}
	if left, _ := cli(ctx, nil, "exec", id, "sh", "-c", "ls /tmp | grep -c apptown-result || true"); strings.TrimSpace(string(left)) != "0" {
		t.Fatalf("left %s result files in the container", left)
	}
}

// The frame runs under the shells and tools of the images copies run in,
// and the receiver accepts what it sends; a failing command sends no
// trailer.
func TestIntegrationAFramedStreamIsVerifiedUnderEachCopyImage(t *testing.T) {
	t.Parallel()
	for _, image := range []string{busybox, "alpine/git:v2.47.2", "postgres:16-alpine", "mariadb:11.4.13"} {
		t.Run(image, func(t *testing.T) {
			t.Parallel()
			var got bytes.Buffer
			r := containerresult.NewReceiver(&got)
			cmd := "printf 'first\\n'; head -c 300000 /dev/zero | tr '\\0' x"
			_, err := docker.CLI{}.Run(context.Background(), "", docker.Command{Args: []string{"run", "--rm", "--network", "none", "--entrypoint", "", image, "sh", "-c", containerresult.Framed(cmd)}, Stdout: r})
			if err != nil || r.Verify() != nil || got.Len() != 300006 || !bytes.HasPrefix(got.Bytes(), []byte("first\nxxx")) {
				t.Fatalf("got %d bytes, %v, %v; want the stream whole and verified", got.Len(), err, r.Verify())
			}
			got.Reset()
			r = containerresult.NewReceiver(&got)
			_, err = docker.CLI{}.Run(context.Background(), "", docker.Command{Args: []string{"run", "--rm", "--network", "none", "--entrypoint", "", image, "sh", "-c", containerresult.Framed("echo some; exit 5")}, Stdout: r})
			if err == nil || !errors.Is(r.Verify(), containerresult.ErrCutShort) {
				t.Fatalf("a failed export: %v, %v; want its exit and no trailer", err, r.Verify())
			}
		})
	}
}
