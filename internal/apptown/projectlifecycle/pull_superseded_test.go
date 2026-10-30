package projectlifecycle

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"testing"
)

func TestPullingAPackClosesTheUpliftToItsOldVersion(t *testing.T) {
	f := newOwnerFixture(t, true)
	ctx := context.Background()
	const name = "review-pack"
	const spec = `{"name":"review-pack"}`
	if v, err := f.ways.PublishPack(ctx, name, "", spec, "fixture"); err != nil || v != 1 {
		t.Fatalf("publish v1: %d %v", v, err)
	}
	p, err := f.lifecycle.Create(ctx, Draft{Slug: "pull-review", Name: "Pull review", Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	applyPacks(t, f, p, name)
	ch := openUpliftTo(t, f, p, name)
	if v, err := f.ways.PublishPackVersion(ctx, name, "", `{"name":"review-pack","guidance":[]}`, "fixture", "second"); err != nil || v != 2 {
		t.Fatalf("publish v2: %d %v", v, err)
	}
	pk, err := f.ways.Pack(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.lifecycle.PullPack(ctx, p, *pk, "bls"); err != nil {
		t.Fatal(err)
	}
	set, err := adoption.ProjectPacksIn(ctx, f.db, p.ID)
	if err != nil || len(set) != 1 || set[0].Version != 2 {
		t.Fatalf("pull did not move membership: %+v %v", set, err)
	}
	if got := stateOf(t, f, workflow.ChangeRef{ID: ch.ID}); got != workflow.ChangeAbandoned {
		t.Fatalf("after pulling v2, uplift targeting v1 is %s, want abandoned", got)
	}
}
