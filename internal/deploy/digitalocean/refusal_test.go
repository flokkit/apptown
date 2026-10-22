package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/digitalocean/dofake"
)

// Only DigitalOcean saying no (a 4xx) is a refusal: the effect did not
// happen. A server error, a timeout or a lost answer leave whether it
// happened unknown, and a caller must not report it as a known failure.
func TestOnlyARefusalSaysTheEffectDidNotHappen(t *testing.T) {
	t.Parallel()
	api := &dofake.API{}
	api.SeedApp("app-1", "", "")
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	c := NewClient("token", srv.URL)
	for _, tc := range []struct {
		status  int
		refused bool
	}{{404, true}, {422, true}, {500, false}, {503, false}} {
		api.Fail("DELETE", "/v2/apps/app-1", tc.status)
		err := c.Call(context.Background(), "DELETE", "/v2/apps/app-1", nil, nil)
		if err == nil || deploy.Refused(err) != tc.refused || deploy.Refused(fmt.Errorf("wrapped: %w", err)) != tc.refused {
			t.Errorf("%d: %v refused=%v", tc.status, err, deploy.Refused(err))
		}
	}
	api.LoseAnswer("DELETE", "/v2/apps/app-1")
	if err := c.Call(context.Background(), "DELETE", "/v2/apps/app-1", nil, nil); err == nil || deploy.Refused(err) {
		t.Errorf("a lost answer: %v", err)
	}
	if deploy.Refused(errors.New("dial tcp: i/o timeout")) || !deploy.Refused(deploy.Terminal(errors.New("rejected"))) || deploy.Refused(nil) {
		t.Error("an error that is not an answer read as one, or a terminal rejection did not")
	}
}

// A new database that was never asked for (its name is taken, or the
// cluster's databases could not be read first) is a refusal: it was not
// made. One whose request got no answer was perhaps made.
func TestANewDatabaseNotAskedForIsARefusal(t *testing.T) {
	t.Parallel()
	api := &dofake.API{}
	api.SeedDatabase(map[string]any{"id": "db-9", "name": "c", "engine": "pg", "status": "online",
		"connection": map[string]any{"uri": "postgresql://doadmin:s@pg.example:25060/defaultdb", "host": "pg.example", "port": 25060}})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	client := NewClient("token", srv.URL)
	m := ManagedData{Client: func(context.Context) (*Client, Config, error) { return client, Config{}, nil },
		OwnDatabase: func(context.Context, string, string, string) error { return nil }}
	ctx := context.Background()
	if err := m.NewDatabase(ctx, "db-9", "taken", "app"); err != nil {
		t.Fatal(err)
	}
	if err := m.NewDatabase(ctx, "db-9", "taken", "app"); !errors.Is(err, ErrDatabaseExists) || !deploy.Refused(err) {
		t.Errorf("a taken name: %v refused=%v", err, deploy.Refused(err))
	}
	api.Fail("GET", "/v2/databases/db-9/dbs", 500)
	if err := m.NewDatabase(ctx, "db-9", "other", "app"); err == nil || !deploy.Refused(err) {
		t.Errorf("databases that could not be read first: %v refused=%v", err, deploy.Refused(err))
	}
	api.Fail("GET", "/v2/databases/db-9/dbs", 0)
	api.LoseAnswer("POST", "/v2/databases/db-9/dbs")
	if err := m.NewDatabase(ctx, "db-9", "lost", "app"); err == nil || deploy.Refused(err) {
		t.Errorf("a request with no answer: %v refused=%v", err, deploy.Refused(err))
	}
}
