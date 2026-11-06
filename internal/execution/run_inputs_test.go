//go:build unix

package execution

import (
	"context"
	"github.com/flokkit/apptown/internal/deploy"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Only the installed/current query's resolved versions are evidence. An unreadable
// snapshot cannot turn a declared range (or an earlier run's snapshot) into fact.
func TestInstalledToolchainRecordsVersionsAndUnreadableAsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      map[string][]string
	}{
		{"resolved", `{"go":[{"version":"1.25.4","requested_version":"1.25"}],"node":[{"version":"22.20.0"},{"version":"24.11.0"}]}`, map[string][]string{"go": {"1.25.4"}, "node": {"22.20.0", "24.11.0"}}},
		{"none", `{}`, map[string][]string{}},
		{"invalid", `{`, nil},
		{"null", `null`, nil},
		{"missing version", `{"go":[{}]}`, map[string][]string{"go": {"unknown"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ToolchainSnapshot), []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			if got := readInstalledToolchain(dir); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
	if got := readInstalledToolchain(t.TempDir()); got != nil {
		t.Fatalf("missing snapshot: %v", got)
	}
}

// Inspect the containers that actually ran, rather than resolving their mutable
// image tags after the fact. A source snapshot must not survive a failed launch.
func TestRunInputsUseContainerDigestsAndDiscardSourceSnapshots(t *testing.T) {
	for _, readable := range []bool{true, false} {
		t.Run(map[bool]string{true: "observed", false: "unreadable"}[readable], func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ToolchainSnapshot), []byte(`{"stale":[{"version":"99"}]}`), 0600); err != nil {
				t.Fatal(err)
			}
			image := "sha256:" + strings.Repeat("a", 64)
			answer := "echo " + image
			if !readable {
				answer = "exit 1"
			}
			binary := filepath.Join(t.TempDir(), "docker")
			body := `#!/bin/sh
case "$1" in
inspect) case "$*" in *'{{.Image}}'*) ` + answer + `;; *OOMKilled*) echo false;; *) echo 'running healthy';; esac;;
run) case "$2" in -d) exit 0;; esac; exit 1;;
*) exit 0;;
esac
`
			writeFakeDocker(t, binary, body)
			req := Request{Workspace: dir, Image: "mutable:tag", Name: "owned", Argv: []string{"false"}, Timeout: time.Minute, CaptureInputs: true, Services: []deploy.ServiceSpec{{Name: "db", Image: "mutable:service"}}}
			out := (Runner{DockerBinary: binary}).Run(context.Background(), Docker, req)
			want := "unknown"
			if readable {
				want = image
			}
			if out.Inputs == nil || out.Inputs.ImageDigest != want || out.Inputs.Services["db"] != want || out.Inputs.Toolchain != nil {
				t.Fatalf("guessed or lost inputs: %+v", out.Inputs)
			}
		})
	}
}
