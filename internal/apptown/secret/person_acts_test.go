package secret

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids"
)

// delivered is every value a delivery hands its consumers, by name.
func delivered(d Delivery) map[string]string {
	out := map[string]string{}
	for _, env := range d.ByConsumer {
		for k, v := range env {
			out[k] = v
		}
	}
	return out
}

// A person's replacement of a bound built-in value is one act with its
// receipt: when the receipt cannot be written, the old value is still the
// one stored, delivered and revealed. And a reveal whose receipt cannot be
// written hands out no value.
//
// Kills: the value committed before the binding and its receipt (delivery
// reads the latest version); a reveal that answers before its receipt.
func TestABuiltInValueAndItsRevealNeedTheirReceipts(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "demo", Name: "Demo"})
	})
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, SpecFile), []byte(testSpec), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewService(db, Config{DataDir: t.TempDir()})
	c := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web"}, {Name: "worker"}}, Init: []appcontract.InitContainer{{Name: "migrate"}}}
	for name, value := range map[string]string{"STRIPE_API_KEY": "stripe-one", "MIGRATION_DB_ROLE": "role-one"} {
		if _, err := s.BindStored(t.Context(), id, "demo", "main", name, value, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	restore := db.FailNextStatement("-- name: AppendReceipt")
	_, err = s.BindStored(t.Context(), id, "demo", "main", "STRIPE_API_KEY", "stripe-two", "alice")
	restore()
	if err == nil {
		t.Fatal("a replacement whose receipt failed reported success")
	}
	if value, _, err := s.Vault.Get(t.Context(), Scope("demo", "main", ""), "STRIPE_API_KEY"); err != nil || value != "stripe-one" {
		t.Fatalf("the stored value after a failed replacement: %q %v", value, err)
	}
	d, err := s.Resolve(t.Context(), id, "demo", "main", ws, c)
	if err != nil {
		t.Fatal(err)
	}
	if got := delivered(d)["STRIPE_API_KEY"]; got != "stripe-one" {
		t.Fatalf("delivery after a failed replacement gives %q", got)
	}
	if got, err := s.Reveal(t.Context(), id, "main", "STRIPE_API_KEY", "alice"); err != nil || got.Value != "stripe-one" {
		t.Fatalf("reveal: %+v %v", got, err)
	}
	restore = db.FailNextStatement("-- name: AppendReceipt")
	got, err := s.Reveal(t.Context(), id, "main", "STRIPE_API_KEY", "alice")
	restore()
	if err == nil || got.Value != "" {
		t.Fatalf("a reveal whose receipt failed answered %q (%v)", got.Value, err)
	}
}

// A deployment's profile decides the secrets it is given: it is not chosen
// without its receipt.
func TestProfileSetNeedsItsReceipt(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "demo", Name: "Demo"})
	})
	if err != nil {
		t.Fatal(err)
	}
	p := NewProfiles(db, nil)
	restore := db.FailNextStatement("-- name: AppendReceipt")
	err = p.Set(t.Context(), id, "main", "large", "alice")
	restore()
	if err == nil {
		t.Fatal("a profile chosen without its receipt reported success")
	}
	if chosen, err := p.Chosen(t.Context(), id, "main"); err != nil || chosen != "" {
		t.Fatalf("the profile after a failed choice: %q %v", chosen, err)
	}
}
