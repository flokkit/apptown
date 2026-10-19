package deployment

import (
	"context"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/poll"
)

// copyCheckSays is a data protection that cannot copy what any deployment
// holds, for why.
type copyCheckSays string

func (c copyCheckSays) CannotCopy(context.Context, *Deployment, *Target) string { return string(c) }

// An intranet release onto a deployment holding data the forge cannot copy
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
	if _, code, err := p.s.ReleaseLAN(t.Context(), &proj, "boss", LANReleaseRequest{Domain: "feedback.example"}); code != 422 || err == nil || !strings.Contains(err.Error(), "cannot copy the data") || !strings.Contains(err.Error(), "async mode") || !strings.Contains(err.Error(), "accept-uncopied-data") {
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
	out, code, err := p.s.ReleaseLAN(t.Context(), &proj, "boss", LANReleaseRequest{Domain: "feedback.example", AcceptUncopiedData: true})
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
	if saved, err := p.projects.ByID(context.Background(), p.project); err != nil || saved.ReleaseDomain != "feedback.example" {
		t.Fatalf("the admitted release's hostname: %q %v", saved.ReleaseDomain, err)
	}
}

// A release names a new hostname and the forge cannot save it: the release
// still serves on it (its stack is labelled with the hostname it was asked
// for), its log says the hostname was not kept for later releases, and no
// receipt says the project's hostname changed.
func TestAnIntranetReleaseServesItsHostnameThoughItWasNotSaved(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	p.land(t)
	proj, err := p.projects.ByID(context.Background(), p.project)
	if err != nil {
		t.Fatal(err)
	}
	restore := p.db.FailNextStatement("UPDATE projects SET release_domain")
	defer restore()
	out, code, err := p.s.ReleaseLAN(t.Context(), &proj, "boss", LANReleaseRequest{Domain: "feedback.example"})
	if err != nil || code != 202 || out.URL != "https://feedback.example" {
		t.Fatalf("the release: %+v %d %v", out, code, err)
	}
	var rel *Release
	if !poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, func() bool {
		var err error
		if rel, err = p.s.Releases.ByNumber(context.Background(), p.project, out.Number); err != nil {
			t.Fatal(err)
		}
		return rel.State != Releasing
	}) {
		t.Fatalf("release %d is still releasing", out.Number)
	}
	p.mu.Lock()
	compose := p.stacks[len(p.stacks)-1]
	p.mu.Unlock()
	if !strings.Contains(compose, "feedback.example") {
		t.Fatalf("the stack does not answer on the hostname the release named:\n%s", compose)
	}
	events, err := p.s.Releases.Events(context.Background(), rel.ID)
	if err != nil {
		t.Fatal(err)
	}
	said := false
	for _, e := range events {
		said = said || strings.Contains(e.Text, "feedback.example is this release's but was not saved for later releases")
	}
	if !said {
		t.Fatalf("the release's log does not say the hostname was not kept: %+v", events)
	}
	if saved, err := p.projects.ByID(context.Background(), p.project); err != nil || saved.ReleaseDomain != "" {
		t.Fatalf("the project's hostname: %q %v", saved.ReleaseDomain, err)
	}
	receipts, err := audit.Recent(context.Background(), p.db, audit.Filter{ProjectID: p.project, KindPrefix: "project.release-domain"})
	if err != nil || len(receipts) != 0 {
		t.Fatalf("a receipt claims the hostname was saved: %+v %v", receipts, err)
	}
}
