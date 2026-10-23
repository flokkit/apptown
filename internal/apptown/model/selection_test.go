package model

import (
	"context"
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
	if p, _, _ := selection.Resolve(t.Context(), projectID, ""); p != "" {
		t.Fatalf("fresh instance resolved a model from nowhere: %q", p)
	}
	// A concrete pattern passes through untouched.
	if p, slot, _ := selection.Resolve(t.Context(), projectID, "prov/model"); p != "prov/model" || slot != "" {
		t.Fatalf("pattern passthrough broken: %q %q", p, slot)
	}
	// Instance default; then a project override outranks it.
	if err := slots.Set(context.Background(), "default", ids.Nil, "", "standard", "inst/model", "fixture"); err != nil {
		t.Fatal(err)
	}
	if p, slot, _ := selection.Resolve(t.Context(), projectID, ""); p != "inst/model" || slot != "default" {
		t.Fatalf("instance default not resolved: %q %q", p, slot)
	}
	if err := slots.Set(context.Background(), "default", projectID, "p", "", "proj/model", "fixture"); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := selection.Resolve(t.Context(), projectID, ""); p != "proj/model" {
		t.Fatalf("project override must win: %q", p)
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
		if err := models.Set(context.Background(), slot, ids.Nil, "", profile, pattern, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	cell("default", "standard", "or/luna")
	cell("fast", "standard", "or/flash")
	cell("worker", "high", "anthropic/sonnet")
	cell("chat", "standard", "anthropic/opus")

	type want struct{ pattern, slot, profile string }
	cases := []struct {
		pid  ids.ID
		req  string
		want want
	}{
		// standard project: worker unmapped in standard → falls to default.
		{plain, "worker", want{"or/luna", "default", "standard"}},
		// high project: the high cell wins.
		{game, "worker", want{"anthropic/sonnet", "worker", "high"}},
		// high project, slot with no high cell: standard column serves it.
		{game, "chat", want{"anthropic/opus", "chat", "standard"}},
		// scout unmapped everywhere → fallback chain scout→fast.
		{game, "scout", want{"or/flash", "fast", "standard"}},
	}
	for _, c := range cases {
		p, s, prof := selection.Resolve(t.Context(), c.pid, c.req)
		if p != c.want.pattern || s != c.want.slot || prof != c.want.profile {
			t.Fatalf("(%d,%q) = %q,%q,%q; want %v", c.pid, c.req, p, s, prof, c.want)
		}
	}
	// An explicit per-project row beats every profile — and says so.
	if err := models.Set(context.Background(), "worker", game, "game", "", "local/pinned", "fixture"); err != nil {
		t.Fatal(err)
	}
	if p, _, prof := selection.Resolve(t.Context(), game, "worker"); p != "local/pinned" || prof != "override" {
		t.Fatalf("project override must win and be named: %q %q", p, prof)
	}
}
