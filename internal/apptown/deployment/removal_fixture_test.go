package deployment

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
)

// The fake provider is injected at the service boundary; ordinary bindings and
// managed needs are created through domain operations.
type removalDriver struct {
	fakeDriver
	calls  atomic.Int32
	remove func(context.Context, deploy.Placement, int64) error
	got    deploy.TakeDown
	taken  deploy.TakenDown
}

func (d *removalDriver) Teardown(ctx context.Context, p deploy.Placement, g int64, td deploy.TakeDown) (deploy.TakenDown, error) {
	d.calls.Add(1)
	d.got = td
	if d.remove != nil {
		return deploy.TakenDown{}, d.remove(ctx, p, g)
	}
	return d.taken, nil
}

// removalFixture is a deployment bound on an App Platform target with a
// managed postgres it made.
func removalFixture(t *testing.T) (*Service, *configurationWorld, *removalDriver, *Deployment) {
	t.Helper()
	s, w, in, _ := configurationFixture(t)
	s.authority = w
	driver := &removalDriver{}
	s.providers.drivers = map[string]DriverFactory{KindDOApp: func(context.Context, *Target, *Deployment) (deploy.Driver, error) { return driver, nil }}
	dep := configureForTest(t, s, in)
	if err := s.Needs.RecordIntent(context.Background(), dep.ID, "postgres", map[string]any{"name": "retained-pg"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Needs.RecordIdentity(context.Background(), dep.ID, "postgres", nil, "db-1", "retained-pg"); err != nil {
		t.Fatal(err)
	}
	return s, w, driver, dep
}

// settleRemoval settles a take-down of dep through Deployment's own
// participants, as data protection does after the provider has deleted
// what it made: selected again under its locks, rechecked, and settled.
func settleRemoval(t *testing.T, s *Service, dep *Deployment, by string, taken deploy.TakenDown) []string {
	t.Helper()
	retained, err := database.Transact(context.Background(), s.db, func(tx *database.Tx) ([]string, error) {
		if _, err := SelectRemovalIn(context.Background(), tx, dep.ProjectID, dep.Name, false); err != nil {
			return nil, err
		}
		return RemoveRecordedIn(context.Background(), tx, dep, false, RemovalSettlement{Target: "cloud", By: access.Principal{Name: by}, Taken: taken, At: time.Now()})
	})
	if err != nil {
		t.Fatal(err)
	}
	return retained
}
