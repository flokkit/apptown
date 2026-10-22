package digitalocean

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/digitalocean/dofake"
)

// DigitalOcean pages every list. An account holding more apps or databases than
// one page must not look smaller than it is: a lookup finds a resource on a
// later page and the inventory counts every one. The fake names the next page
// on the public API host, so following the link's path on the configured base
// also proves the token is never sent to a host the client was not given.
func TestListsFollowEveryPageOnTheConfiguredHost(t *testing.T) {
	t.Parallel()
	api := &dofake.API{}
	const n = 205 // one more page than per_page=200 holds
	for i := range n {
		api.SeedDatabase(map[string]any{"id": fmt.Sprintf("db-%d", i), "name": fmt.Sprintf("forge-db-%d", i), "status": "online"})
		api.SeedApp(fmt.Sprintf("app-%d", i), "dep-1", "")
	}
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	c := NewClient("dop_v1_test", server.URL)
	ctx := context.Background()

	got, err := (&Driver{}).findDatabase(ctx, c, "forge-db-204")
	if err != nil || got == nil || got.ID != "db-204" {
		t.Fatalf("a database on the second page: got %+v, %v", got, err)
	}
	resources, err := Inventory{Client: func(context.Context) (*Client, Config, error) { return c, Config{}, nil }}.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	for _, r := range resources {
		count[r.Kind]++
	}
	if count["app"] != n || count["database"] != n {
		t.Fatalf("inventory counted %d apps and %d databases, want %d of each", count["app"], count["database"], n)
	}
}
