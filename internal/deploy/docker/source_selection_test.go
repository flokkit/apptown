package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type selectedSourceBoot func(BootRequest, *appcontract.Contract) (BootResult, error)

func (f selectedSourceBoot) Boot(_ context.Context, req BootRequest, c *appcontract.Contract) (BootResult, error) {
	return f(req, c)
}

func TestDockerDeploymentUsesSelectedSource(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	contract := func(marker string) string {
		return "app:\n  web:\n    image: example/app:1\n    public: true\n    environment:\n      MARKER: " + marker + "\n"
	}
	write := func(marker string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, ".apptown", "app.yaml"), []byte(contract(marker)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("A")
	for _, args := range [][]string{{"init"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.invalid"}, {"add", "."}, {"commit", "-m", "A"}} {
		if _, err := gitForDockerTest(repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	selected, err := ReadWorkspaceContract(repo)
	if err != nil {
		t.Fatal(err)
	}
	art, cleanup, err := PrepareSource(context.Background(), repo, "HEAD", "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	write("B")
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "B"}} {
		if _, err := gitForDockerTest(repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	var served string
	daemon := &fakeDaemon{}
	dk := Scope{Client: daemon}
	d := &Driver{Allowed: AuthorisedWhenRequested, ServingLabel: "stable-serving",
		Scope: func(context.Context) (Scope, error) { return dk, nil },
		Boot: selectedSourceBoot(func(bt BootRequest, c *appcontract.Contract) (BootResult, error) {
			if bt.Workspace == repo {
				t.Fatal("driver received moving checkout")
			}
			daemon.exists, daemon.running, daemon.image = true, true, "sha256:built"
			daemon.labels = bt.Identity.Labels()
			served = c.PublicPart().Env["MARKER"]
			return BootResult{HostPort: 1234, Stack: ResolvedStack{Plan: &appcontract.Plan{Parts: []appcontract.Part{{Name: "web", Public: true, Image: "sha256:built"}}}}}, nil
		}),
		URL: func(string, string, int) (string, error) { return "http://app", nil }, AfterRelease: func(deploy.Placement) {},
	}
	_, err = d.Reconcile(context.Background(), deploy.Placement{ID: idstest.N(1), Label: "1", Name: "main"}, 1, art, "selected-A", func(string, ...any) {}, func(map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if served != selected.PublicPart().Env["MARKER"] {
		t.Fatalf("selected A, checkout advanced to B, deployed %q", served)
	}
	if strings.TrimSpace(served) == "" {
		t.Fatal("no boot")
	}
}
