package agentenv

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A step container's apptown.created label is AppTown's time it was given:
// maintenance judges a container's age by AppTown's clock, so the label
// must be on that clock too.
//
// Kills: the label stamped by the real clock.
func TestAStepContainersCreatedLabelIsAppTownsTime(t *testing.T) {
	t.Parallel()
	at := time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)
	req := Decorate(execution.Request{Name: "step"}, at, "")
	if want := "apptown.created=" + at.Format(time.RFC3339); !slices.Contains(req.Labels, want) {
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
	game, shop := mount(WithCacheScope(context.Background(), idstest.N(1))), mount(WithCacheScope(context.Background(), idstest.N(2)))
	if !slices.Equal(game, []string{"apptown-project-cache-00000000-0001-7000-8000-000000000000:/apptown-cache"}) || !slices.Equal(shop, []string{"apptown-project-cache-00000000-0002-7000-8000-000000000000:/apptown-cache"}) {
		t.Fatalf("project caches: game %v, shop %v", game, shop)
	}
	if none := Decorate(execution.Request{Name: "step"}, time.Time{}, CacheVolume(context.Background())); len(none.Mounts) != 0 || slices.ContainsFunc(none.Env, func(e string) bool { return strings.Contains(e, "/apptown-cache") }) {
		t.Fatalf("unscoped work mounts no cache: %+v", none)
	}
}

func TestProjectCacheRetirementAndDiscoveryKeepExactIdentity(t *testing.T) {
	t.Parallel()
	owned := "apptown-project-cache-00000000-0001-7000-8000-000000000000"
	other := "apptown-project-cache-00000000-0002-7000-8000-000000000000"
	for _, failure := range []string{"", "volume is in use", "daemon unavailable"} {
		t.Run(failure, func(t *testing.T) {
			d := newDockerFake()
			var removed []string
			d.SetReply(func(args []string) ([]byte, error, bool) {
				if len(args) > 1 && args[0] == "volume" && args[1] == "ls" {
					if failure == "daemon unavailable" {
						return nil, errors.New(failure), true
					}
					return []byte(strings.Join([]string{owned, other, "apptown-cache-game", "apptown-cache-console-c1", "apptown-data-store", "apptown-project-cache-not-a-uuid"}, "\n")), nil, true
				}
				if len(args) == 3 && args[0] == "volume" && args[1] == "rm" {
					removed = append(removed, args[2])
					if failure != "" {
						return []byte(failure), errors.New("removal failed"), true
					}
					return []byte("Error response from daemon: No such volume: " + args[2]), errors.New("missing"), true
				}
				t.Errorf("unexpected daemon command %v", args)
				return nil, nil, true
			})
			dk := docker.Scope{Client: d}
			found, err := ProjectCaches(t.Context(), dk)
			if failure == "daemon unavailable" {
				if err == nil || len(found) != 0 {
					t.Fatalf("unavailable inventory became %v: %v", found, err)
				}
			} else if err != nil || !slices.Equal(found, []string{owned, other}) {
				t.Fatalf("owned inventory %v: %v", found, err)
			}
			for range 2 {
				err = RemoveProjectCache(t.Context(), dk, idstest.N(1))
				if (err != nil) != (failure != "") {
					t.Fatalf("removal failure=%q err=%v", failure, err)
				}
			}
			if !slices.Equal(removed, []string{owned, owned}) {
				t.Fatalf("removed foreign resources: %v", removed)
			}
			if err := RemoveProjectCache(t.Context(), dk, ids.Nil); err == nil || len(removed) != 2 {
				t.Fatal("nil owner admitted removal")
			}
		})
	}
	if got := CacheVolume(WithCacheScope(t.Context(), ids.Nil)); got != "" {
		t.Fatalf("nil identity mounts %s", got)
	}
}

func newDockerFake() *dockerfake.Daemon {
	return dockerfake.New()
}
