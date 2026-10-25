package deployment

import (
	"context"
	"strings"
	"testing"
)

// copyCheckSays is a data protection that cannot copy what any deployment
// holds, for why.
type copyCheckSays string

func (c copyCheckSays) CannotCopy(context.Context, *Deployment, *Target) string { return string(c) }

// An intranet release onto a deployment holding data AppTown cannot copy
// is refused before a number is minted, saying why and how to go on; a
// person may release anyway, and the release's record says who chose to
// and why the data could not be copied.
func TestAnIntranetReleaseOverUncopiedDataIsAPersonsChoice(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	p.land(t)
	p.s.copyCheck = copyCheckSays("AppTown cannot back up deployments in the Portainer environment \"local\": it is an edge environment in async mode")
	proj, err := p.projects.ByID(context.Background(), p.project)
	if err != nil {
		t.Fatal(err)
	}
	before, err := p.s.Releases.ForProject(context.Background(), p.project, 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, code, err := p.s.ReleaseLAN(t.Context(), &proj, "boss", LANReleaseRequest{AddressRevision: new(int64), Domain: "feedback.example"}); code != 422 || err == nil || !strings.Contains(err.Error(), "cannot copy the data") || !strings.Contains(err.Error(), "async mode") || !strings.Contains(err.Error(), "accept-uncopied-data") {
		t.Fatalf("a release over uncopied data: %d %v", code, err)
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
	out, code, err := p.s.ReleaseLAN(t.Context(), &proj, "boss", LANReleaseRequest{AddressRevision: new(int64), Domain: "feedback.example", AcceptUncopiedData: true})
	if err != nil || code != 202 {
		t.Fatalf("the person's choice: %d %v", code, err)
	}
	rel, err := p.s.Releases.ByNumber(context.Background(), p.project, out.Number)
	if err != nil {
		t.Fatal(err)
	}
	events, err := p.s.Releases.Events(context.Background(), rel.ID)
	if err != nil || len(events) == 0 || !strings.Contains(events[0].Text, "released by boss accepting that AppTown cannot copy the data") || !strings.Contains(events[0].Text, "async mode") {
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
func TestIntranetAddressReceiptFailureRefusesTheWholeRelease(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	p.land(t)
	proj, err := p.projects.ByID(t.Context(), p.project)
	if err != nil {
		t.Fatal(err)
	}
	before, err := p.s.Releases.ForProject(t.Context(), p.project, 50)
	if err != nil {
		t.Fatal(err)
	}
	restore := p.db.FailNextStatement("-- name: AppendReceipt")
	defer restore()
	out, _, err := p.s.ReleaseLAN(t.Context(), &proj, "boss", LANReleaseRequest{AddressRevision: new(int64), Domain: "feedback.example"})
	if err == nil || out != nil {
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

func TestIntranetDeploymentDoesNotInventAServingAddress(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	// sql-guarantee: retain legacy project-address evidence without creating desired deployment routing.
	if _, err := p.db.Exec(`UPDATE projects SET release_domain=$2 WHERE id=$1`, p.project, "feedback.example"); err != nil {
		t.Fatal(err)
	}
	cfg, err := p.s.IntranetConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	dep, err := p.s.intranetDeployment(t.Context(), p.project, cfg)
	if err != nil || dep.URL != "" {
		t.Fatalf("initial address: %+v %v", dep, err)
	}
	// sql-guarantee: changing raw legacy evidence cannot invent a serving observation.
	if _, err := p.db.Exec(`UPDATE projects SET release_domain=$2 WHERE id=$1`, p.project, "changed.example"); err != nil {
		t.Fatal(err)
	}
	dep, err = p.s.intranetDeployment(t.Context(), p.project, cfg)
	if err != nil || dep.URL != "" {
		t.Fatalf("existing deployment address changed: %+v %v", dep, err)
	}
}
