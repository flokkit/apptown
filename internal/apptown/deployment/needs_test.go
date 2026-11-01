package deployment

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestManagedNeedStoreRequiresAnIntent(t *testing.T) {
	t.Parallel()
	store := NewNeedStore(testDB(t), nil)
	if err := store.RecordIdentity(context.Background(), idstest.N(987), "postgres", nil, "db-1", "example"); err == nil {
		t.Fatal("missing intent was treated as a saved identity")
	}
}

// A managed need's established identity (an instance, a database and a
// user) is never replaced: an intent for another cluster, database or user
// is refused and writes nothing; an intent for the same one leaves the
// fulfilled record as it is; a record that is only an intent may still be
// replaced. Copies take what the record names, so a release that changed
// it and then failed would leave every copy on the wrong database.
func TestAManagedNeedsEstablishedIdentityIsNeverReplaced(t *testing.T) {
	t.Parallel()
	s, _, _, dep := removalFixture(t)
	ctx := context.Background()
	handle := map[string]any{"id": "db-1", "name": "forge-shop-cloud", "database": "app", "user": "app", "version": "16"}
	requested := func(name, db, user string) map[string]any {
		return map[string]any{"name": name, "engine": "pg", "database": db, "user": user}
	}
	// Only an intent and an identity so far: a new intent replaces it.
	if err := s.needs.RecordIntent(ctx, dep.ID, "postgres", requested("forge-shop-cloud", "ledger", "app")); err != nil {
		t.Fatalf("an intent over an unfinished one: %v", err)
	}
	if err := s.needs.recordFulfilled(ctx, dep.ID, "postgres", nil, handle); err != nil {
		t.Fatal(err)
	}
	for name, asked := range map[string]map[string]any{
		"another database": requested("forge-shop-cloud", "next", "app"),
		"another user":     requested("forge-shop-cloud", "app", "other"),
		"another cluster":  requested("forge-renamed-cloud", "app", "app"),
	} {
		if err := s.needs.RecordIntent(ctx, dep.ID, "postgres", asked); !errors.Is(err, errIdentityHeld) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := s.needs.RecordIntent(ctx, dep.ID, "postgres", requested("forge-shop-cloud", "app", "app")); err != nil {
		t.Fatalf("the same identity asked again: %v", err)
	}
	insts, err := s.needs.ForDeployment(ctx, dep.ID)
	if err != nil || len(insts) != 1 || !maps.Equal(insts[0].Handle, handle) || insts[0].Status != NeedReady {
		t.Fatalf("the record still names the established identity: %+v %v", insts, err)
	}
}
