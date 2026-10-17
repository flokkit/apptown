package deployment

import (
	"context"
	"errors"
	"testing"

	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// copyStarter is data protection's side of a start from a copy as these
// tests see it: it counts fills and answers a receipt for the set.
type copyStarter struct{ fills int }

func (c *copyStarter) CheckStart(context.Context, ids.ID, ids.ID, string) error { return nil }
func (c *copyStarter) FillStart(_ context.Context, in StartFill) (StartReceipt, error) {
	c.fills++
	return StartReceipt{Set: in.Deployment.StartFrom, FromDeployment: "prod", FromProject: in.Deployment.ProjectID}, nil
}

// startFromWorld is a deployment that has never run, starting from set 7,
// and a release admitted to fill set admitted.
func startFromWorld(t *testing.T, starter CopyStarter, admitted ids.ID) (*Service, *Deployment, NumberedRelease) {
	t.Helper()
	s, _, _, dep := removalFixture(t)
	s.starter = starter
	if n, err := deploymentsql.New(s.db.SQL()).SetStartFrom(context.Background(), deploymentsql.SetStartFromParams{BackupID: idstest.N(7), ID: dep.ID}); err != nil || n != 1 {
		t.Fatalf("choosing the start: %d %v", n, err)
	}
	rel, err := s.Releases.Start(context.Background(), ReleaseDraft{Source: ReleaseFromBranch, ProjectID: dep.ProjectID, DeploymentID: dep.ID, Deployment: dep.Name, Revision: "r1", Actor: "alice"}, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deploymentsql.New(s.db.SQL()).InsertReleaseRequest(context.Background(), deploymentsql.InsertReleaseRequestParams{ReleaseID: rel.ID, TargetID: dep.TargetID, AttributesJson: []byte(`{}`), StartFromBackup: admitted}); err != nil {
		t.Fatal(err)
	}
	return s, dep, rel
}

func startedFrom(t *testing.T, s *Service, dep *Deployment) bool {
	t.Helper()
	current, err := s.Deployments.ByID(context.Background(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	return current.StartedFrom.Valid
}

// A release fills only the set it was admitted to fill: another set is
// refused before data protection is asked, and nothing is recorded.
//
// Kills: dropping the match of the release to its admitted set.
func TestAReleaseAdmittedForAnotherSetFillsNothing(t *testing.T) {
	t.Parallel()
	starter := &copyStarter{}
	s, dep, rel := startFromWorld(t, starter, idstest.N(8))
	t0, _ := s.Targets.ByID(context.Background(), dep.TargetID)
	if err := s.startFromCopy(context.Background(), rel, dep, t0, "alice", func(string, ...any) {}); !errors.Is(err, ErrStartFrom) {
		t.Fatalf("a release admitted for set 8 filled set 7: %v", err)
	}
	if starter.fills != 0 || startedFrom(t, s, dep) {
		t.Fatalf("filled %d, started %v", starter.fills, startedFrom(t, s, dep))
	}
}

// A release admitted for the set is filled by data protection once, and
// Deployment records the start with its receipt; with no data protection
// the start is refused, never taken as a deployment with no data.
//
// Kills: recording the start without the fill; treating an absent port as
// a completed fill.
func TestAStartFromACopyIsFilledThenRecorded(t *testing.T) {
	t.Parallel()
	starter := &copyStarter{}
	s, dep, rel := startFromWorld(t, starter, idstest.N(7))
	t0, _ := s.Targets.ByID(context.Background(), dep.TargetID)
	s.starter = nil
	if err := s.startFromCopy(context.Background(), rel, dep, t0, "alice", func(string, ...any) {}); !errors.Is(err, errNoCopyStarter) || startedFrom(t, s, dep) {
		t.Fatalf("no data protection: %v, started %v", err, startedFrom(t, s, dep))
	}
	s.starter = starter
	if err := s.startFromCopy(context.Background(), rel, dep, t0, "alice", func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if starter.fills != 1 || !startedFrom(t, s, dep) {
		t.Fatalf("filled %d, started %v", starter.fills, startedFrom(t, s, dep))
	}
	got := receipts(t, s.db, "deployment.started-from-copy")
	if len(got) != 1 {
		t.Fatalf("receipts: %+v", got)
	}
	if p := receiptPayload(t, got[0]); p["backup"] != idstest.N(7).String() || p["release"] != float64(rel.Number) {
		t.Fatalf("receipt: %+v", p)
	}
	// Filled once: a later release does not fill it again.
	if err := s.startFromCopy(context.Background(), rel, dep, t0, "alice", func(string, ...any) {}); err != nil || starter.fills != 1 {
		t.Fatalf("a started deployment was filled again: %v, %d", err, starter.fills)
	}
}
