package deployment

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// copyCheckSays is a data protection that cannot copy what any deployment
// holds, for why.
type copyCheckSays string

func (c copyCheckSays) CannotCopy(context.Context, *Deployment, *Target) string { return string(c) }

// A default release onto a deployment holding data AppTown cannot copy
// is refused before a number is minted, saying why and how to go on; a
// person may release anyway, and the release's record says who chose to
// and why the data could not be copied.
func TestADefaultReleaseOverUncopiedDataIsAPersonsChoice(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	p.land(t)
	p.s.copyCheck = copyCheckSays("AppTown cannot back up deployments in the Portainer environment \"local\": it is an edge environment in async mode")
	selected := p.chooseDefault(t)
	hostname := "feedback.example"
	before, err := p.s.Releases.ForProject(context.Background(), p.project, 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.s.RequestDefaultRelease(t.Context(), DefaultReleaseSelection{ProjectID: p.project, ExpectedRevision: &selected.Revision, AddressRevision: new(int64), CustomHostname: &hostname}); !errors.Is(err, ErrRefusedContract) || !strings.Contains(err.Error(), "cannot copy the data") || !strings.Contains(err.Error(), "async mode") || !strings.Contains(err.Error(), "accept-uncopied-data") {
		t.Fatalf("a release over uncopied data: %v", err)
	}
	// Refused before any effect: no release, and the hostname it named is
	// not the project's.
	after, err := p.s.Releases.ForProject(context.Background(), p.project, 50)
	if err != nil || len(after) != len(before) {
		t.Fatalf("a refused release minted one: %d before, %d after (%v)", len(before), len(after), err)
	}
	if saved, err := p.projects.ByID(context.Background(), p.project); err != nil || saved.ReleaseDomain != "" {
		t.Fatalf("a refused release saved its hostname: %q %v", saved.ReleaseDomain, err)
	}
	out, err := p.s.RequestDefaultRelease(t.Context(), DefaultReleaseSelection{ProjectID: p.project, ExpectedRevision: &selected.Revision, AddressRevision: new(int64), CustomHostname: &hostname, AcceptUncopiedData: true})
	if err != nil {
		t.Fatalf("the person's choice: %v", err)
	}
	rel, err := p.s.Releases.ByNumber(context.Background(), p.project, out.Number)
	if err != nil {
		t.Fatal(err)
	}
	events, err := p.s.Releases.Events(context.Background(), rel.ID)
	if err != nil || len(events) == 0 || !strings.Contains(events[0].Text, "released by alice accepting that AppTown cannot copy the data") || !strings.Contains(events[0].Text, "async mode") {
		t.Fatalf("the release's record of the choice: %+v %v", events, err)
	}
	dep, err := p.s.Deployments.ByName(t.Context(), p.project, "lan")
	if err != nil {
		t.Fatal(err)
	}
	if saved, err := p.s.ReadAddress(t.Context(), p.project, dep.ID); err != nil || saved.CustomHostname != "feedback.example" {
		t.Fatalf("admitted deployment hostname: %+v %v", saved, err)
	}
}

// Address persistence and its receipt are part of admission; no partial release
// may escape when that receipt fails.
func TestDefaultAddressReceiptFailureRefusesTheWholeRelease(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	p.land(t)
	selected := p.chooseDefault(t)
	hostname := "feedback.example"
	before, err := p.s.Releases.ForProject(t.Context(), p.project, 50)
	if err != nil {
		t.Fatal(err)
	}
	restore := p.db.FailNextStatement("-- name: AppendReceipt")
	defer restore()
	out, err := p.s.RequestDefaultRelease(t.Context(), DefaultReleaseSelection{ProjectID: p.project, ExpectedRevision: &selected.Revision, AddressRevision: new(int64), CustomHostname: &hostname})
	if err == nil || !out.ID.IsZero() {
		t.Fatalf("failed address receipt admitted release: %+v %v", out, err)
	}
	after, err := p.s.Releases.ForProject(t.Context(), p.project, 50)
	if err != nil || len(after) != len(before) {
		t.Fatalf("failed admission kept release: %d %d %v", len(before), len(after), err)
	}
	dep, err := p.s.Deployments.ByName(t.Context(), p.project, "lan")
	if err != nil {
		t.Fatal(err)
	}
	reading, err := p.s.ReadAddress(t.Context(), p.project, dep.ID)
	if err != nil || reading.Revision != 0 || len(reading.ReservedHosts) != 0 {
		t.Fatalf("failed admission kept address: %+v %v", reading, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.stacks) != 0 {
		t.Fatal("failed admission reached provider")
	}
}

func TestChoosingAReleaseDefaultDoesNotInventAServingAddress(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	// sql-guarantee: retain legacy project-address evidence without creating desired deployment routing.
	if _, err := p.db.Exec(`UPDATE projects SET release_domain=$2 WHERE id=$1`, p.project, "feedback.example"); err != nil {
		t.Fatal(err)
	}
	selected := p.chooseDefault(t)
	dep, err := p.s.Deployments.ByID(t.Context(), selected.DeploymentID)
	if err != nil || dep.URL != "" {
		t.Fatalf("initial address: %+v %v", dep, err)
	}
	// sql-guarantee: changing raw legacy evidence cannot invent a serving observation.
	if _, err := p.db.Exec(`UPDATE projects SET release_domain=$2 WHERE id=$1`, p.project, "changed.example"); err != nil {
		t.Fatal(err)
	}
	dep, err = p.s.Deployments.ByID(t.Context(), selected.DeploymentID)
	if err != nil || dep.URL != "" {
		t.Fatalf("existing deployment address changed: %+v %v", dep, err)
	}
}
