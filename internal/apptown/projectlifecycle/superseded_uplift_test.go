package projectlifecycle

import (
	"context"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
)

// openUpliftTo opens the project's adoption change toward the named packs.
func openUpliftTo(t *testing.T, f ownerFixture, p *project.Project, packs ...string) *workflow.Change {
	t.Helper()
	ctx := context.Background()
	var picks []adoption.PackPick
	for _, name := range packs {
		pk, err := f.ways.Pack(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		picks = append(picks, adoption.PackPick{Pack: pk.Name, Version: pk.Version})
	}
	target, err := f.adoption.Prepare(ctx, p.ID, adoption.Request{Packs: picks})
	if err != nil {
		t.Fatal(err)
	}
	driver := access.WithCaller(ctx, access.Caller{Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: "eval", Via: "eval/1", ProjectID: p.ID}, Verified: true, Receipt: "eval/1"})
	changes := workflow.NewChanges(f.db, access.Checked{Authority: identity.Scoped(f.identity, "work"), Projects: project.New(f.db, nil)}, nil)
	ch, err := changes.Uplift(driver, workflow.Uplift{ProjectID: p.ID, ExpectedRevision: target.ExpectedRevision, Acceptance: workflow.Acceptance{TargetDigest: target.Digest},
		Words: []string{strings.Join(packs, ", ")}, Target: copyTargetRecorder{adoption: f.adoption, candidate: *target, copy: p.ID}})
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

func applyPacks(t *testing.T, f ownerFixture, p *project.Project, names ...string) {
	t.Helper()
	var packs []*wow.Pack
	for _, name := range names {
		pk, err := f.ways.Pack(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		packs = append(packs, pk)
	}
	if _, err := f.lifecycle.ApplyPack(adoption.WithSystemAuthority(context.Background(), p.ID), p, packs, "bls"); err != nil {
		t.Fatal(err)
	}
}

func stateOf(t *testing.T, f ownerFixture, id workflow.ChangeRef) workflow.ChangeState {
	t.Helper()
	ch, err := workflow.ReadChangeIn(context.Background(), f.db, id.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ch.State
}

// Changing the pack set closes the open adoption change aimed at a pack the
// set no longer has, saying why, and leaves one that still fits open.
//
// Kills: an uplift to a retired pack left open (it can never pass and the
// person cannot close it), and a still-valid uplift closed with it.
func TestChangingThePackSetClosesTheUpliftItSupersedes(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, true)
	mk := func(slug string) *project.Project {
		p, err := f.lifecycle.Create(context.Background(), Draft{Slug: slug, Name: slug, Actor: "bls"})
		if err != nil {
			t.Fatal(err)
		}
		applyPacks(t, f, p, "html-game")
		return p
	}

	gone := mk("retired")
	retired := openUpliftTo(t, f, gone, "html-game", "postgres")
	applyPacks(t, f, gone, "vite-react")
	if got := stateOf(t, f, workflow.ChangeRef{ID: retired.ID}); got != workflow.ChangeAbandoned {
		t.Fatalf("the uplift to a pack the project left is %s, want abandoned", got)
	}
	closed := lifecycleReceipts(t, f.db, "change.abandoned")
	if len(closed) != 1 || !strings.Contains(string(closed[0].Payload), "superseded: the project now takes") || !strings.Contains(string(closed[0].Payload), "vite-react") {
		t.Fatalf("the close was recorded as %+v", closed)
	}

	kept := mk("kept")
	valid := openUpliftTo(t, f, kept, "html-game")
	applyPacks(t, f, kept, "html-game", "postgres")
	if got := stateOf(t, f, workflow.ChangeRef{ID: valid.ID}); got != workflow.ChangeInProgress {
		t.Fatalf("an uplift to a pack the project still takes is %s, want open", got)
	}
}
