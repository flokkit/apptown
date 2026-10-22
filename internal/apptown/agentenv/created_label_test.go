package agentenv

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
)

// A step container's forge.created label is AppTown's time it was given:
// maintenance judges a container's age by AppTown's clock, so the label
// must be on that clock too.
//
// Kills: the label stamped by the real clock.
func TestAStepContainersCreatedLabelIsAppTownsTime(t *testing.T) {
	t.Parallel()
	at := time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)
	req := Decorate(execution.Request{Name: "step"}, at, "")
	if want := "forge.created=" + at.Format(time.RFC3339); !slices.Contains(req.Labels, want) {
		t.Fatalf("labels %v, want %s", req.Labels, want)
	}
}

// One project's steps share a cache volume; another project's never see it,
// and work with no project mounts none at all (cold, shared with nobody).
//
// Kills: the one AppTown-cache volume every project's steps wrote to.
func TestAStepsCacheIsItsProjectsAlone(t *testing.T) {
	t.Parallel()
	mount := func(ctx context.Context) []string {
		return Decorate(execution.Request{Name: "step"}, time.Time{}, CacheVolume(ctx)).Mounts
	}
	game, shop := mount(WithCacheScope(context.Background(), "game")), mount(WithCacheScope(context.Background(), "shop"))
	if !slices.Equal(game, []string{"apptown-cache-game:/apptown-cache"}) || !slices.Equal(shop, []string{"apptown-cache-shop:/apptown-cache"}) {
		t.Fatalf("project caches: game %v, shop %v", game, shop)
	}
	if none := Decorate(execution.Request{Name: "step"}, time.Time{}, CacheVolume(context.Background())); len(none.Mounts) != 0 || slices.ContainsFunc(none.Env, func(e string) bool { return strings.Contains(e, "/apptown-cache") }) {
		t.Fatalf("unscoped work mounts no cache: %+v", none)
	}
}
