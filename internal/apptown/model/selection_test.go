package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func TestResolveModelSlot(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	slots := New(db)
	selection := NewSelection(slots)
	projectID, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	// Nothing configured: unresolvable — the first-run "silently inert"
	// condition the setup card exists to catch.
	if p, _, _, err := selection.Resolve(t.Context(), projectID, ""); err != nil || p != "" {
		t.Fatalf("fresh instance resolved a model from nowhere: %q", p)
	}
	// A concrete pattern passes through untouched.
	if p, slot, _, err := selection.Resolve(t.Context(), projectID, "prov/model"); err != nil || p != "prov/model" || slot != "" {
		t.Fatalf("pattern passthrough broken: %q %q", p, slot)
	}
	// An undeclared profile uses the standard main-strong choice.
	if err := slots.Set(context.Background(), "main-strong", "standard", "inst/model", "fixture"); err != nil {
		t.Fatal(err)
	}
	if p, slot, _, err := selection.Resolve(t.Context(), projectID, ""); err != nil || p != "inst/model" || slot != "main-strong" {
		t.Fatalf("standard main-strong not resolved: %q %q", p, slot)
	}
}

func TestModelProfileResolution(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	models := New(db)
	selection := NewSelection(models)
	projects := project.New(db, nil)
	create := func(slug, name string) ids.ID {
		t.Helper()
		id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
			return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: name})
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	// This test exercises the stock slot fallback policy.
	if err := models.SeedFallbacks(context.Background()); err != nil {
		t.Fatal(err)
	}
	plain := create("plain", "Plain")
	game := create("game", "Game")
	if err := projects.SetModelProfile(context.Background(), plain, "standard"); err != nil {
		t.Fatal(err)
	}
	if err := projects.SetModelProfile(context.Background(), game, "high"); err != nil {
		t.Fatal(err)
	}
	cell := func(slot, profile, pattern string) {
		t.Helper()
		if err := models.Set(context.Background(), slot, profile, pattern, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	cell("main-strong", "standard", "or/luna")
	cell("fast", "standard", "or/flash")
	cell("worker", "high", "anthropic/sonnet")
	cell("chat", "standard", "anthropic/opus")

	type want struct{ pattern, slot, profile string }
	cases := []struct {
		pid  ids.ID
		req  string
		want want
	}{
		// standard project: worker unmapped in standard → falls to main-strong.
		{plain, "worker", want{"or/luna", "main-strong", "standard"}},
		// high project: the high cell wins.
		{game, "worker", want{"anthropic/sonnet", "worker", "high"}},
		// high project, slot with no high cell: standard column serves it.
		{game, "chat", want{"anthropic/opus", "chat", "standard"}},
		// scout unmapped everywhere → fallback chain scout→fast.
		{game, "scout", want{"or/flash", "fast", "standard"}},
	}
	for _, c := range cases {
		p, s, prof, err := selection.Resolve(t.Context(), c.pid, c.req)
		if err != nil || p != c.want.pattern || s != c.want.slot || prof != c.want.profile {
			t.Fatalf("(%d,%q) = %q,%q,%q; want %v", c.pid, c.req, p, s, prof, c.want)
		}
	}
}

// Persisted requests and in-flight steps must fail at resolution after the
// cutover, even when a main model is configured.
func TestRetiredModelRequestsFailWithActionableError(t *testing.T) {
	t.Parallel()
	slots := New(testDB(t))
	if err := slots.SetDefault(t.Context(), "vendor/main"); err != nil {
		t.Fatal(err)
	}
	selection := NewSelection(slots)
	for _, requested := range []string{"default", "primary-fast"} {
		pattern, slot, profile, err := selection.Resolve(t.Context(), ids.Nil, requested)
		want := fmt.Sprintf("unknown model choice %q: edit the model request to use main-strong or main-fast; the other basic choices are secondary-strong and secondary-fast", requested)
		if err == nil || err.Error() != want || pattern != "" || slot != "" || profile != "" {
			t.Fatalf("%s: resolved %q/%q/%q with %v; want %s", requested, pattern, slot, profile, err, want)
		}
	}
}
