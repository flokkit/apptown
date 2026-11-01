package deployment

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// Read the actual selected Git object, so a fixture cannot hide a fallback
// to the branch tip or the mutable checkout during release preflight.
type committedReleaseSources struct{ digestSources }

func (s committedReleaseSources) Contract(ctx context.Context, _ ReleaseProject, revision string) (*appcontract.Contract, string, error) {
	raw, err := releaseGit(ctx, s.repo, "show", revision+":.apptown/app.yaml")
	if err != nil {
		return nil, "", err
	}
	contract, err := appcontract.Parse([]byte(raw))
	return contract, revision, err
}

func TestReleasePreflightUsesCommittedDeclaration(t *testing.T) {
	t.Parallel()
	declared := strings.Replace(digestAppYAML, "init:\n", "needs:\n  unknown-service: {}\ninit:\n", 1)
	p := newPromotionTargetsWith(t, false, declared)
	p.publish(t)
	selected := p.chooseDefault(t)
	clean := strings.Replace(string(p.contract), "needs:\n  unknown-service: {}\n", "", 1)
	if err := os.WriteFile(filepath.Join(p.repo, ".apptown", "app.yaml"), []byte(clean), 0600); err != nil {
		t.Fatal(err)
	}
	p.s.release.Sources = committedReleaseSources{p.sources}
	_, err := p.s.RequestDefaultRelease(t.Context(), DefaultReleaseSelection{ProjectID: p.project, ExpectedRevision: &selected.Revision})
	if !errors.Is(err, ErrRefusedContract) || !strings.Contains(err.Error(), "unknown-service") {
		t.Fatalf("preflight used live checkout: %v", err)
	}
	if _, n := p.lastStack(t); n != 0 {
		t.Fatal("refused preflight reached Portainer")
	}
}
