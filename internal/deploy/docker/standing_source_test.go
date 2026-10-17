package docker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/appcontract"
)

func sourceCalls(f *fakeClient, prefix string) []string {
	var out []string
	for _, args := range f.args {
		line := strings.Join(args, " ")
		if strings.HasPrefix(line, prefix) {
			out = append(out, line)
		}
	}
	return out
}

func TestStandingDeployHonorsDeclaredSource(t *testing.T) {
	t.Parallel()
	for _, build := range []bool{false, true} {
		t.Run(fmt.Sprint(build), func(t *testing.T) {
			f := &fakeClient{}
			f.reply = func(req Command) ([]byte, error) {
				if len(req.Args) > 0 && req.Args[0] == "port" {
					return []byte("0.0.0.0:43210\n"), nil
				}
				return nil, nil
			}
			dk := Scope{Client: f}
			ws := t.TempDir()
			if err := os.WriteFile(filepath.Join(ws, "Dockerfile"), []byte("FROM alpine:3.20\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			c := &appcontract.Part{Image: "registry.example/app:chosen", Port: 8080}
			if build {
				c.Image, c.Build = "", "."
			}
			if _, err := NewHost(Dependencies{}).StartAppContainer(t.Context(), BootRequest{Docker: dk, Name: "forge-env-app-main", Workspace: ws, Persistent: true}, c, "", nil); err != nil {
				t.Fatal(err)
			}
			if got := len(sourceCalls(f, "build ")); (got == 1) != build {
				t.Fatalf("build=%v, calls %v", build, f.args)
			}
			want := "registry.example/app:chosen"
			if build {
				want = BuildTag("forge-env-app-main")
			}
			runs := sourceCalls(f, "run -d --name forge-env-app-main")
			if len(runs) != 1 || !strings.HasSuffix(runs[0], " "+want) {
				t.Fatalf("wrong source: %v", runs)
			}
			if !build && !hasStandingCall(f.args, "pull "+want) {
				t.Fatal("declared image was not pulled")
			}
		})
	}
}
