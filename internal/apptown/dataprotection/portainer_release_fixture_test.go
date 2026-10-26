//go:build integration

package dataprotection

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
)

// The published stock image is a fixture prerequisite. Release admission,
// current authority, provider effects and settlement still use their owners.
type portainerRecoveryRelease struct {
	slug, repo, contract, revision string
	bundle                         ids.ID
	secrets                        map[string]string
}

func (r *portainerRecoveryRelease) Project(_ context.Context, id ids.ID) (deployment.ReleaseProject, error) {
	return deployment.ReleaseProject{Application: deployment.Application{ID: id, Slug: r.slug}, Repository: r.repo}, nil
}

func (r *portainerRecoveryRelease) Contract(_ context.Context, _ deployment.ReleaseProject, revision string) (*appcontract.Contract, string, error) {
	if revision != r.revision {
		return nil, "", fmt.Errorf("fixture has no revision %s", revision)
	}
	c, err := appcontract.Parse([]byte(r.contract))
	return c, revision, err
}

func (r *portainerRecoveryRelease) ResolveRelease(_ context.Context, _ deployment.Application, name, _ string, _ *appcontract.Contract) (deploy.ConsumerSecrets, []string, error) {
	value, ok := r.secrets[name]
	if !ok {
		return nil, nil, fmt.Errorf("fixture has no secrets for %s", name)
	}
	return secret.Delivery{ByConsumer: map[string]map[string]string{"web": {"DEPLOYMENT_SECRET": value}}}, []string{"DEPLOYMENT_SECRET"}, nil
}

func (r *portainerRecoveryRelease) prepare(t *testing.T, s *both, pid ids.ID, contract, platform string) {
	t.Helper()
	r.repo = t.TempDir()
	r.contract = contract
	if err := os.MkdirAll(filepath.Join(r.repo, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.repo, ".apptown/app.yaml"), []byte(contract), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "Portainer recovery app"}} {
		cmd := gittest.Command(args...)
		cmd.Dir = r.repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	cmd := gittest.Command("rev-parse", "HEAD")
	cmd.Dir = r.repo
	revision, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	r.revision = strings.TrimSpace(string(revision))
	image := stock.Images().DF
	registry, _, ok := strings.Cut(image, "@")
	if !ok {
		t.Fatal("fixture image must be pinned by digest")
	}
	claim := &claimedAttempt{db: s.db, project: pid, run: ids.New(time.Now()), step: ids.New(time.Now()), at: 1}
	r.bundle, err = s.Bundles.Cut(t.Context(), pid, r.revision, map[string]string{"web": image, "worker": image}, map[string]string{"migrate": image}, []byte(contract), deployment.BundleOrigin{Registry: registry, Platform: platform, Build: deployment.BundleBuild{Worker: "published-stock-image-fixture", Role: "fixture"}, By: claim})
	if err != nil {
		t.Fatal(err)
	}
}

func (r *portainerRecoveryRelease) apply(t *testing.T, s *both, pid ids.ID, name, value string) deployment.AcceptedRelease {
	t.Helper()
	r.secrets[name] = value
	accepted, err := s.RequestRelease(t.Context(), deployment.DeploySelection{ProjectID: pid, DeploymentName: name, BundleID: r.bundle})
	if err != nil {
		t.Fatal(err)
	}
	executionErr := s.ExecuteRelease(t.Context(), accepted.ID)
	release, err := s.Releases.ByNumber(t.Context(), pid, accepted.Number)
	if err != nil {
		t.Fatal(err)
	}
	if executionErr != nil || release.State != deployment.Released {
		events, readErr := s.Releases.Events(t.Context(), release.ID)
		t.Fatalf("release %s: execution=%v state=%s events=%+v read=%v", name, executionErr, release.State, events, readErr)
	}
	return accepted
}
