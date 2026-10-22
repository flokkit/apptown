//go:build integration

package docker

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/check"
)

// Failed reads must not let differing trees count as checked. The command
// wrappers run in the production helper image, through its real ash shell.
func TestIntegrationTheCopyCheckFailsOnAFailedRead(t *testing.T) {
	setup := `set -e
mkdir /from /to /failbin
printf data > /from/kept
ln -s kept /from/link
cp -a /from/. /to/
`
	for _, tc := range []struct{ name, command, body, change string }{
		{"metadata", "stat", "echo 'metadata read failed' >&2; exit 17", "chown 70:70 /to/kept\n"},
		{"link", "readlink", "echo 'link read failed' >&2; exit 18", "ln -sfn missing /to/link\n"},
		{"content", "sha256sum", `case "$PWD" in /from|/to) echo 'content read failed' >&2; exit 19;; esac
exec /bin/busybox sha256sum "$@"`, "printf dat0 > /to/kept\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := setup + tc.change + "cat > /failbin/" + tc.command + " <<'EOF'\n#!/bin/sh\n" + tc.body + "\nEOF\nchmod +x /failbin/" + tc.command + "\nPATH=/failbin:$PATH\n" + sameTree
			out, err := exec.Command("docker", "run", "--rm", "--network", "none", "--entrypoint", "sh", check.ShImage, "-c", script).CombinedOutput()
			if !strings.Contains(string(out), "read failed") {
				t.Fatalf("read failure was not exercised: %v: %s", err, out)
			}
			if err == nil {
				t.Fatalf("a differing copy with failed %s reads was accepted: %s", tc.command, out)
			}
		})
	}
}

// A target ending in a newline names a different path. Capturing readlink
// must not strip that distinction and report the copy checked.
func TestIntegrationTheCopyCheckComparesLinkTargetsByteForByte(t *testing.T) {
	setup := `set -e
mkdir /from /to
printf data > /from/kept
ln -s kept /from/link
cp -a /from/. /to/
rm /to/link
target=$(printf 'kept\nx'); target=${target%x}
ln -s "$target" /to/link
`
	out, err := exec.Command("docker", "run", "--rm", "--network", "none", "--entrypoint", "sh", check.ShImage, "-c", setup+sameTree).CombinedOutput()
	if err == nil {
		t.Fatalf("a copy with a changed link target was accepted: %s", out)
	}
}

// The entry path and target must remain distinct fields in the digest.
func TestIntegrationTheCopyCheckKeepsALinksPathApartFromItsTarget(t *testing.T) {
	setup := `set -e
mkdir /from /to
printf data > /from/kept
ln -s kept '/from/link -> part'
cp -a /from/. /to/
`
	for _, tc := range []struct{ name, change string }{
		{"correct", ""},
		{"different", "rm '/to/link -> part'\nln -s 'part -> kept' /to/link\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := exec.Command("docker", "run", "--rm", "--network", "none", "--entrypoint", "sh", check.ShImage, "-c", setup+tc.change+sameTree).CombinedOutput()
			if tc.change == "" && err != nil {
				t.Fatalf("correct copy refused: %v: %s", err, out)
			}
			if tc.change != "" && err == nil {
				t.Fatalf("different link path and broken target accepted: %s", out)
			}
		})
	}
}
