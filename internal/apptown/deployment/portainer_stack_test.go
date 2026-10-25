package deployment

import (
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/trust/trusttest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The stack stages the admitted commit, and includes only the trust bundle
// for external services, even when the repository branch has moved.
// Not parallel: it installs the process's trust.
func TestDefaultReleaseStagesSelectedCommitAfterBranchAdvances(t *testing.T) {
	company, local := trusttest.New(t, "Company CA"), trusttest.New(t, "Local CA")
	trusttest.Install(t, company.Bundle(t), local.Bundle(t))
	p := newPromotionTargetsWith(t, false, strings.Replace(digestAppYAML, "GREETING: hello", "GREETING: original", 1))
	bundle := p.publish(t)
	later := strings.Replace(string(p.contract), "GREETING: original", "GREETING: later", 1)
	if err := os.WriteFile(filepath.Join(p.repo, ".apptown", "app.yaml"), []byte(later), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", ".apptown/app.yaml"}, {"-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "Later contract"}} {
		if _, err := releaseGit(t.Context(), p.repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	p.s.release.Sources = committedReleaseSources{p.sources}
	rel := p.releaseDefault(t)
	compose, _ := p.lastStack(t)
	if rel.State != Released || rel.CommitSHA != bundle.ContractRevision || !strings.Contains(compose, "original") || strings.Contains(compose, "later") {
		t.Fatalf("release staged another revision: %+v; compose: %s", rel, compose)
	}
	if !strings.Contains(compose, "APPTOWN_TRUST") || !strings.Contains(compose, strings.Split(string(company.PEM()), "\n")[1]) || strings.Contains(compose, strings.Split(string(local.PEM()), "\n")[1]) {
		t.Fatalf("the stack does not give the app the company's certificate alone:\n%s", compose)
	}
	ran := 0
	for _, attempt := range attemptsIn(t, p.db) {
		in, err := p.s.Outcomes.InputsFor(t.Context(), Attempt{ID: attempt.ID, DeploymentID: attempt.DeploymentID, Generation: attempt.Generation})
		if err != nil {
			t.Fatal(err)
		}
		if in.ReleaseID == rel.ID && in.Revision == bundle.ContractRevision {
			ran++
		}
	}
	wantCount(t, "attempts admitted with the selected revision", ran, 1)
}

// Portainer runs a bundle's static image with no command of its own: the
// image carries the command that serves it.
func TestAnIntranetBundleRunsItsStaticImageAsItsRecordSays(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("app:\n  web:\n    public: true\n    static: site\n    ports: [\"8080\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	b := &ReleaseBundle{ID: idstest.N(3), Parts: map[string]string{"web": "registry.example/p@sha256:" + strings.Repeat("a", 64)}}
	plan, err := bundleApp(appcontract.Prepare(c, nil), stackImages{Parts: b.Parts, Init: b.Init})
	if err != nil {
		t.Fatal(err)
	}
	if web := plan.PublicPart(); !web.ServesStatic || web.Image != b.Parts["web"] || len(web.Command) != 0 {
		t.Fatalf("the static part: %+v", web)
	}
}
