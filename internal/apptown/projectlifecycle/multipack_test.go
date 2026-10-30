package projectlifecycle

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/wow"
)

// A new project given several packs gets their union: the one starter
// among them (the backend's), the one toolchain (the backend's mise.toml),
// and every pack recorded, with the platform pack beside them.
//
// Kills: starter files or the toolchain read from a pack other than the
// one that has them (a UI pack has neither), or a pack left unrecorded.
func TestApplyingABackendWithAUIPackSeedsTheBackendsStarter(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, true)
	p, err := f.lifecycle.Create(context.Background(), Draft{Slug: "shop", Name: "Shop", Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.lifecycle.InitializeRepository(t.Context(), p, "main", ""); err != nil {
		t.Fatal(err)
	}
	var packs []*wow.Pack
	for _, name := range []string{"vite-react", "server-base", "go", "postgres"} {
		pk, err := f.ways.Pack(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		packs = append(packs, pk)
	}
	got, err := f.lifecycle.ApplyPack(adoption.WithSystemAuthority(context.Background(), p.ID), p, packs, "bls")
	if err != nil {
		t.Fatal(err)
	}
	if got.Toolchain != "go" || !slices.Contains(got.Seeded, ".apptown/app.yaml") || !slices.Contains(got.Seeded, "Dockerfile") {
		t.Fatalf("toolchain %q, seeded %v; want go's mise.toml and go's starter", got.Toolchain, got.Seeded)
	}
	receipt, err := f.adoption.CurrentReceipt(t.Context(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{adoption.PlatformPack, "go", "postgres", "server-base", "vite-react"}; !slices.Equal(receipt.Receipt.Packs, want) {
		t.Fatalf("the receipt names %v, want %v", receipt.Receipt.Packs, want)
	}
}

// Two packs that each carry starter files are refused together before
// anything is written: a project has one starter, and no pack outranks
// another.
//
// Kills: one starter silently laid over the other.
func TestTwoStartersAreRefusedTogether(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, true)
	p, err := f.lifecycle.Create(context.Background(), Draft{Slug: "games", Name: "Games", Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.adoption.CurrentReceipt(t.Context(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var packs []*wow.Pack
	for _, name := range []string{"html-game", "video-game"} {
		pk, err := f.ways.Pack(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		packs = append(packs, pk)
	}
	if _, err := f.lifecycle.ApplyPack(adoption.WithSystemAuthority(context.Background(), p.ID), p, packs, "bls"); !errors.Is(err, adoption.ErrNotAdoptable) {
		t.Fatalf("two starters: %v", err)
	}
	if _, err := f.lifecycle.Create(context.Background(), Draft{Slug: "games-too", Name: "Games", Actor: "bls", Packs: []string{"html-game", "video-game"}}); !errors.Is(err, adoption.ErrNotAdoptable) || slugTaken(t, f.db, "games-too") {
		t.Fatalf("creating with two starters: %v", err)
	}
	after, err := f.adoption.CurrentReceipt(t.Context(), p.ID)
	if err != nil || after.ID != before.ID {
		t.Fatalf("a refused application moved the adoption: %+v %v", after, err)
	}
}
