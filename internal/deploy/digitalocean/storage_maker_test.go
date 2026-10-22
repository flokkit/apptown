package digitalocean

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
)

// A crash after both holdings and the secret were recorded must still be
// cleaned up at the next release or ordinary take-down, as the plan promises.
func TestALeftMakerKeyIsDeletedByTheNextReleaseOrTakeDown(t *testing.T) {
	for _, next := range []string{"release", "take-down"} {
		t.Run(next, func(t *testing.T) {
			a := newSpacesAccount(t)
			if _, _, err := a.fulfil(t); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			leaked, err := a.driver.mintKey(ctx, mustClient(t, a), "forge-maker-shop-main", spacesGrant{Permission: "fullaccess"})
			if err != nil {
				t.Fatal(err)
			}
			if next == "release" {
				if _, _, err := a.fulfil(t); err != nil {
					t.Fatal(err)
				}
			} else {
				owned, _ := a.holdings.Owned(ctx)
				if _, err := a.driver.Teardown(ctx, deploy.Placement{Name: "main"}, 0, deploy.TakeDown{Owned: owned}); err != nil {
					t.Fatal(err)
				}
			}
			for _, k := range a.api.Snapshot().SpacesKeys {
				if k["access_key"] == leaked.AccessKey {
					t.Errorf("%s left the full-access maker key %s live", next, leaked.AccessKey)
				}
			}
		})
	}
}

// A take-down that cannot revoke its temporary full-access key must return
// that credential as Left so settlement retains its identity and warning.
func TestATakeDownReportsAMakerKeyItCouldNotDelete(t *testing.T) {
	a := newSpacesAccount(t)
	if _, _, err := a.fulfil(t); err != nil {
		t.Fatal(err)
	}
	access := fmt.Sprintf("DOFAKE%d", a.api.Snapshot().SpacesKeysMade+1)
	a.api.Fail("DELETE", "/v2/spaces/keys/"+access, 500)
	owned, _ := a.holdings.Owned(context.Background())
	taken, err := a.driver.Teardown(context.Background(), deploy.Placement{Name: "main"}, 0, deploy.TakeDown{Owned: owned, Synthetic: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.keyNames()) != 1 || a.keyNames()[0] != "apptown-maker-shop-main" {
		t.Fatalf("expected the failed deletion to leave a maker key, got %v", a.keyNames())
	}
	if !slices.ContainsFunc(taken.Left, func(l deploy.Left) bool { return l.Handle["name"] == access }) {
		t.Fatalf("live full-access key %s omitted from take-down: %+v", access, taken)
	}
}

// A take-down that keeps the bucket still sweeps a maker key an earlier
// operation left, and one it cannot delete is reported left, named.
func TestAKeptBucketsTakeDownReportsALeftMakerKeyItCannotDelete(t *testing.T) {
	t.Parallel()
	a := newSpacesAccount(t)
	if _, _, err := a.fulfil(t); err != nil {
		t.Fatal(err)
	}
	k, err := a.driver.mintKey(context.Background(), mustClient(t, a), "forge-maker-shop-main", spacesGrant{Permission: "fullaccess"})
	if err != nil {
		t.Fatal(err)
	}
	a.api.Fail("DELETE", "/v2/spaces/keys/"+k.AccessKey, 500)
	owned, _ := a.holdings.Owned(context.Background())
	taken, err := a.driver.Teardown(context.Background(), deploy.Placement{Name: "main"}, 0, deploy.TakeDown{Owned: owned})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(taken.Left, func(l deploy.Left) bool {
		return l.Handle["name"] == k.AccessKey && strings.Contains(l.Why, "every bucket")
	}) {
		t.Fatalf("the left maker key is not reported: %+v", taken)
	}
}
