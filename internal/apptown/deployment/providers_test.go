package deployment

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A provider driver is never built for a deployment whose project cannot be
// read: the DigitalOcean and S3 bucket drivers name their resources after the
// project's slug, and with an empty one they would act on resources that are
// not the project's.
//
// Kills: logging the failed lookup and building the driver with an empty
// slug; treating a missing row as found.
func TestADriverIsNotBuiltWithoutItsProject(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := New(context.Background(), db, assembled(db, testRuntime{Projects: project.New(db, nil)}, Dependencies{}))
	pid := createTestProject(t, db, "p", "P")
	cloud := &Target{ID: idstest.N(98), Name: "cloud", Kind: KindDOApp, Provider: "do-syd"}
	missing := &Deployment{ID: idstest.N(1), ProjectID: idstest.N(999999), Name: "prod"}
	for _, target := range []*Target{cloud, {ID: idstest.N(99), Name: "bucket", Kind: KindS3Bucket}} {
		t.Run(string(target.Kind), func(t *testing.T) {
			driver, err := s.providers.Driver(context.Background(), target, missing)
			if err == nil || driver != nil {
				t.Fatalf("a driver for a deployment of a missing project: %v, %v", driver, err)
			}
			if fault.KindOf(err) != fault.NotFound {
				t.Fatalf("a missing project reads as %v: %v", fault.KindOf(err), err)
			}
		})
	}
	// The same factories build a driver for a project that is there.
	for _, target := range []*Target{cloud, {ID: idstest.N(99), Name: "bucket", Kind: KindS3Bucket}} {
		if driver, err := s.providers.Driver(context.Background(), target, &Deployment{ID: idstest.N(1), ProjectID: pid, Name: "prod"}); err != nil || driver == nil {
			t.Fatalf("%s driver for a project that is there: %v, %v", target.Kind, driver, err)
		}
	}
}
