package projectlifecycle

import (
	"strings"
	"testing"
)

func TestTheUpgradeRenamesACustomBlocksChecks(t *testing.T) {
	f := newOwnerFixture(t, false)
	ctx := t.Context()
	b, err := f.ways.CreateBlock(ctx, "house/runtime", "Read .forge/app.yaml", "", "admin")
	if err != nil {
		t.Fatal(err)
	}
	checks := `[{"name":"contract-present","expects":"commit .forge/app.yaml","evaluator":"sh","sh":"test -f .forge/app.yaml","severity":"high","scope":"tree"}]`
	if err := f.ways.BumpBlock(ctx, b.ID, "Read .forge/app.yaml", checks, "admin", b.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := f.lifecycle.UpgradeToAppTown(ctx, "admin", upgradeWork{}); err != nil {
		t.Fatal(err)
	}
	after, err := f.ways.BlockByID(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.ways.BlockVersionChecks(ctx, b.ID, after.LatestVersion)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, ".forge/") || !strings.Contains(got, "test -f .apptown/app.yaml") {
		t.Fatalf("upgraded checks still require the retired contract: %s", got)
	}
}
