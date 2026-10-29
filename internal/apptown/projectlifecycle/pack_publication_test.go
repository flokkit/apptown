package projectlifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/adoption"
)

func TestProjectCreationAndReceiptCommitTogether(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, true)
	t.Cleanup(f.db.FailNextStatement("INSERT INTO project_adoptions"))
	if _, err := f.lifecycle.Create(context.Background(), Draft{Slug: "atomic", Name: "Atomic", Actor: "bls"}); err == nil {
		t.Fatal("receipt failure hidden")
	}
	if slugTaken(t, f.db, "atomic") {
		t.Fatal("receipt failure left its project")
	}
	p, err := f.lifecycle.Create(context.Background(), Draft{Slug: "atomic", Name: "Atomic", Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	ad, err := f.adoption.CurrentReceipt(t.Context(), p.ID)
	if err != nil || ad == nil || ad.Revision != 1 || ad.Kind != adoption.AdoptionCreate || len(ad.Receipt.Workflows) == 0 {
		t.Fatalf("initial receipt: %+v %v", ad, err)
	}
}

func TestPackApplyRecordsPinsAndRollsBackSetup(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, true)
	p, err := f.lifecycle.Create(context.Background(), Draft{Slug: "pack-target", Name: "Target", Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.adoption.CurrentReceipt(t.Context(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := f.ways.Pack(t.Context(), "go-small")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.db.FailNextStatement("INSERT INTO project_packs"))
	ctx := adoption.WithSystemAuthority(context.Background(), p.ID)
	if _, err := f.lifecycle.ApplyPack(ctx, p, pk, "bls"); err == nil {
		t.Fatal("membership failure hidden")
	}
	failed, err := f.adoption.CurrentReceipt(ctx, p.ID)
	if err != nil || failed.ID != before.ID {
		t.Fatalf("failed pack moved adoption: %+v %v", failed, err)
	}
	for _, table := range []string{"guidance_imports", "project_lanes", "project_packs", "policies", "project_pi_extensions"} {
		// sql-guarantee: A failed pack apply must leave no setup row in any of the five tables, including ones no single owner read lists together.
		ownerCount(t, f.db, 0, `SELECT COUNT(*) FROM `+table+` WHERE project_id=$1`, p.ID)
	}
	if _, err := f.lifecycle.ApplyPack(ctx, p, pk, "bls"); err != nil {
		t.Fatal(err)
	}
	after, err := f.adoption.CurrentReceipt(ctx, p.ID)
	if err != nil || after.Revision != before.Revision+1 {
		t.Fatalf("one explicit adoption: %+v %v", after, err)
	}
	if packs, err := adoption.ProjectPacksIn(ctx, f.db, p.ID); err != nil || len(packs) != 1 {
		t.Fatalf("pack memberships: %+v %v", packs, err)
	}
	for _, block := range after.Receipt.Guidance {
		if block.Version == 0 {
			t.Fatalf("unpinned block: %+v", block)
		}
	}
	current, err := f.adoption.Union(ctx, p.ID, after)
	if err != nil || adoption.Digest(current) != after.Digest {
		t.Fatalf("rows disagree with receipt: %+v vs %+v: %v", current, after, err)
	}
}

// A creation that names a pack the instance does not offer (withdrawn, or
// no such pack) is refused before anything is made, so `apptown new --pack`
// fails without leaving a packless project behind; an offered pack's
// creation goes ahead.
func TestCreatingWithAPackNotOfferedCreatesNothing(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, true)
	if err := f.ways.OfferPack(t.Context(), "go-small", false, "bls"); err != nil {
		t.Fatal(err)
	}
	for _, pack := range []string{"go-small", "no-such-pack"} {
		_, err := f.lifecycle.Create(context.Background(), Draft{Slug: "packless", Name: "Packless", Actor: "bls", Pack: pack})
		if !errors.Is(err, adoption.ErrPackNotOffered) {
			t.Fatalf("%s: got %v, want the pack refused", pack, err)
		}
		if slugTaken(t, f.db, "packless") {
			t.Fatalf("%s: the refused creation left its project", pack)
		}
	}
	if _, err := f.lifecycle.Create(context.Background(), Draft{Slug: "packed", Name: "Packed", Actor: "bls", Pack: "django-htmx"}); err != nil {
		t.Fatalf("an offered pack's creation: %v", err)
	}
}
