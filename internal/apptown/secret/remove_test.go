package secret

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// A project's bindings, profiles and the stored values only it bound go;
// another project's stay, including a value both projects shared.
func TestRemoveProjectInKeepsOtherProjects(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	a, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "a", Name: "A"})
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "b", Name: "B"})
	})
	if err != nil {
		t.Fatal(err)
	}
	v, g := NewVault(db), NewBindings(db)
	for _, s := range []struct {
		pid   ids.ID
		scope string
		name  string
	}{{a, "env:a:main", "OWN"}, {a, "shared:x", "SHARED"}, {b, "shared:x", "SHARED"}, {b, "env:b:main", "OWN"}} {
		if _, _, err := v.Get(t.Context(), s.scope, s.name); errors.Is(err, ErrNotStored) {
			if _, err := v.Put(t.Context(), s.scope, s.name, "value", "bls"); err != nil {
				t.Fatal(err)
			}
		}
		if err := g.Put(t.Context(), s.pid, Binding{Deployment: "main", Name: s.name, Store: StoreInternal, Ref: s.scope, Version: "1"}); err != nil {
			t.Fatal(err)
		}
	}
	p := NewProfiles(db)
	_ = p.Set(context.Background(), a, "main", "default")
	_ = p.Set(context.Background(), b, "main", "default")
	if err := db.InTx(context.Background(), func(tx *database.Tx) error { return RemoveProjectIn(context.Background(), tx, a) }); err != nil {
		t.Fatal(err)
	}
	if mine, _ := g.For(t.Context(), a, "main"); len(mine) != 0 {
		t.Fatal("a's bindings are gone")
	}
	if theirs, _ := g.For(t.Context(), b, "main"); len(theirs) != 2 {
		t.Fatalf("b keeps its bindings: %d", len(theirs))
	}
	if _, _, err := v.Get(t.Context(), "env:a:main", "OWN"); !errors.Is(err, ErrNotStored) {
		t.Fatalf("a's own value is gone: %v", err)
	}
	if _, _, err := v.Get(t.Context(), "shared:x", "SHARED"); err != nil {
		t.Fatalf("the shared value stays for b: %v", err)
	}
	if chosen, _ := p.Chosen(t.Context(), a, "main"); chosen != "" {
		t.Fatal("a's profile is gone")
	}
	if chosen, _ := p.Chosen(t.Context(), b, "main"); chosen != "default" {
		t.Fatal("b's profile stays")
	}
}
