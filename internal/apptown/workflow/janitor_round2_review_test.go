package workflow

import (
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestReviewPurgePreservesUsableSourceComparison(t *testing.T) {
	t.Parallel()
	s, ch, repo, _, running := automatedFixture(t)
	main := gittest.Run(t, repo, "rev-parse", "HEAD")
	dep, bundle := idstest.N(100), idstest.N(101)
	running.rows = []RunningImages{{DeploymentID: dep, BundleID: bundle, Images: map[string]string{"web": "digest-a"}}}
	now := time.Now().UTC()
	recordObservations(t, s, ch, []conformance.ScanObservation{
		{RunID: idstest.N(102), Subject: conformance.ScanSubject{On: "source", Commit: main}, Scanner: "osv", Verdict: "pass", ObservedAt: now},
		{RunID: idstest.N(103), Subject: conformance.ScanSubject{On: "running", DeploymentID: dep, BundleID: bundle, Part: "web", Digest: "digest-a"}, Scanner: "trivy", Verdict: "fail", ObservedAt: now, Findings: []conformance.Finding{{Finding: check.Finding{Identifiers: []string{"CVE-OS"}, Package: &check.PackageRef{Name: "libssl", Ecosystem: "Debian"}}}}},
	})
	assertKnown := func() {
		t.Helper()
		got, err := s.LatestMainScans(t.Context(), ch.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Known || len(got.Running) != 1 || len(got.Running[0].ImageOnlyFindings) != 1 {
			t.Fatalf("usable comparison lost: known=%v source=%d running=%+v", got.Known, len(got.Source), got.Running)
		}
	}
	assertKnown()
	if _, err := s.Abandon(t.Context(), StopWork{ChangeID: ch.ID, Reason: "observation finished"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeAutomatedChanges(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Change(t.Context(), ch.ID); err == nil {
		t.Fatal("execution record survived purge")
	}
	assertKnown()
	running.rows[0].Images["web"] = "digest-b"
	got, err := s.LatestMainScans(t.Context(), ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Known || got.Reason != "no running image scan" {
		t.Fatalf("missing current running scan: %+v", got)
	}
	// No deployments still needs the running scan's explicit answer that
	// nothing is deployed; absence of an observation is not that answer.
	running.rows = nil
	got, err = s.LatestMainScans(t.Context(), ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Known || got.Reason != "no running image scan" {
		t.Fatalf("missing running scan with no deployments: %+v", got)
	}
}
