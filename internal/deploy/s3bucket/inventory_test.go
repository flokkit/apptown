package s3bucket

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/s3bucket/s3fake"
	"github.com/flokkit/apptown/internal/deploy/staticsite"
)

// Spaces has no bucket tags, so a bucket AppTown publishes to carries a
// private object naming the installation. The website does not serve it,
// and the clean-up of old files never removes it.
func TestAPublishedBucketCarriesThisInstallationsPrivateMark(t *testing.T) {
	w := newWorld(t)
	for _, rev := range []string{"r1", "r2", "r3"} {
		if _, err := w.release(map[string]string{"index.html": rev}, rev); err != nil {
			t.Fatal(err)
		}
	}
	bucket := w.handle["bucket"].(string)
	mark, ok := w.store.Get(bucket, staticsite.InstanceKey)
	if !ok || string(mark.Body) != "inst-a" || mark.Public {
		t.Fatalf("the bucket's mark after three releases: %+v %v", mark, ok)
	}
	if status, _ := get(t, w.handle["address"].(string)+"/"+staticsite.InstanceKey); status == http.StatusOK {
		t.Fatal("the website serves the installation's mark")
	}
}

// Only a bucket carrying this installation's mark can be not accounted for:
// another installation's, an unmarked one (made before the mark, or for a
// deployment on another kind of target) and someone else's confirm records
// only. A mark that cannot be read fails the inventory; it is never absent.
func TestBucketInventoryCountsOnlyThisInstallationsMark(t *testing.T) {
	w := newWorld(t)
	if _, err := w.release(v1Files, "r1"); err != nil {
		t.Fatal(err)
	}
	ours := w.handle["bucket"].(string)
	w.store.AddBucket("apptown-theirs-1")
	w.store.Put("apptown-theirs-1", staticsite.InstanceKey, s3fake.Object{Body: []byte("inst-b")})
	w.store.AddBucket("apptown-unmarked-1")
	w.store.AddBucket("someone-elses")
	d := w.driver()
	inv := Inventory{Client: d.Client, Installation: "inst-a"}
	got, err := inv.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mine := map[string]bool{}
	for _, r := range got {
		if r.Kind != "bucket" {
			t.Fatalf("not a bucket: %+v", r)
		}
		mine[r.Name] = !slices.Contains(r.Tags, deploy.TagConfirmsOnly)
	}
	want := map[string]bool{ours: true, "apptown-theirs-1": false, "apptown-unmarked-1": false, "someone-elses": false}
	if len(mine) != len(want) {
		t.Fatalf("listed %v", mine)
	}
	for name, ok := range want {
		if mine[name] != ok {
			t.Errorf("%s: this installation's %v, want %v", name, mine[name], ok)
		}
	}
	w.store.Fail("GET", "apptown-unmarked-1", staticsite.InstanceKey, 500)
	if _, err := inv.Inventory(context.Background()); err == nil {
		t.Fatal("an unreadable mark read as absent")
	}
}
