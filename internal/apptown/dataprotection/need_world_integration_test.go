//go:build integration

package dataprotection

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockertest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
)

// needWorld is a project on the real daemon whose contract declares one
// stock need, with two standing deployments, main and fresh, each running
// that need as a release does (its own network and named data volume).
// It returns the need's container per deployment; everything it makes is
// removed when the test ends. The need's image is pulled first, so a stock
// image no registry serves fails here and not only on a fresh host.
func needWorld(t *testing.T, need string) (*both, ids.ID, map[string]string) {
	t.Helper()
	db := testDB(t)
	slug := fmt.Sprintf("it%s%d", need, os.Getpid())
	pid := createTestProject(t, db, slug, need+" backup")
	dir := t.TempDir()
	catalog := dependency.NewCatalog(db, nil)
	if _, _, err := catalog.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	kind, err := catalog.ByCapability(t.Context(), need)
	if err != nil {
		t.Fatal(err)
	}
	dk := docker.Scope{Client: docker.CLI{Binary: "docker"}}
	s := assembled(db, testRuntime{Scope: dk, DataDir: dir, Needs: catalog, Secrets: secret.NewService(db, secret.Config{DataDir: dir}), ChownImage: stock.Images().Sh, BuiltInTarget: "local"}, deployment.Dependencies{Releases: deployment.ReleaseDependencies{Sources: &releasedContracts{}}})
	target, err := s.Targets.Put(context.Background(), &deployment.Target{Name: "local", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "workspaces", "project-"+slug, ".apptown")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "app:\n  web:\n    image: registry.example/" + slug + ":1\n    ports: [\"8080\"]\n    public: true\nneeds:\n  " + need + ": {}\n"
	if err := os.WriteFile(filepath.Join(repo, "app.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	services, _, err := dependency.LocalRuntime{Catalog: catalog}.Fulfil(t.Context(), []string{need}, nil)
	if err != nil || len(services) != 1 {
		t.Fatalf("the stock %s: %v %v", need, services, err)
	}
	// Pulled only when missing: CI pulls the heavy tiers' images before the
	// tests (deploy/ci-heavy/test-images.txt), and a test that pulls again
	// fails on a registry error.
	if _, err := dockertest.CombinedOutput("image", "inspect", services[0].Image); err != nil {
		if out, err := dockertest.CombinedOutput("pull", services[0].Image); err != nil {
			t.Fatalf("the stock %s image %s cannot be pulled: %v %s", need, services[0].Image, err, out)
		}
	}
	containerOf := map[string]string{}
	for _, name := range []string{"main", "fresh"} {
		dep, err := s.Deployments.EnsureStanding(t.Context(), pid, name, target.ID, deployment.Attributes{}.WithDefaults())
		if err != nil {
			t.Fatal(err)
		}
		names := hostNamesOf(t, dep, slug)
		stack := names.Stack()
		containerOf[name] = names.Service(need)
		t.Cleanup(func() {
			_ = dockertest.Run("rm", "-f", names.Service(need))
			_ = dockertest.Run("volume", "rm", names.NeedVolume(need))
			_ = dockertest.Run("network", "rm", names.Network())
		})
		runRelease(t, s, dep, "r1", yaml)
		// Started as a release starts it: with the deployment's own
		// credentials, so the two deployments' differ.
		services, _, err := dependency.LocalRuntime{Catalog: catalog, Credentials: s.Service.env.NeedCredentials(deployment.NeedSecretsScope(dep.ID))}.Fulfil(t.Context(), []string{need}, nil)
		if err != nil {
			t.Fatal(err)
		}
		owner := s.Deployments.OwnerOf(dep)
		own := docker.Ownership{Record: func(o deploy.Owned) error { return owner.Created(context.Background(), o) }, Needs: map[string]string{need: kind.Local.Data}}
		if _, err := docker.EnsureStandingServices(context.Background(), dk, stack, services, "synthetic", own); err != nil {
			t.Fatal(err)
		}
	}
	return s, pid, containerOf
}
