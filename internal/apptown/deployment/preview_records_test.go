package deployment

import (
	"context"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestPreviewDeploymentsAreRowsWhileTheyRun(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := New(context.Background(), db, Dependencies{})
	target, err := s.Targets.Put(context.Background(), &Target{Name: "local", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Deployments.RecordPreview(context.Background(), pid, idstest.N(7), target.ID, docker.PreviewChangeName(idstest.N(7)), 40001, "http://x", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Deployments.RecordPreview(context.Background(), pid, idstest.N(7), target.ID, docker.PreviewChangeName(idstest.N(7)), 40001, "http://x", nil); err != nil {
		t.Fatal(err)
	} // a reboot is the next generation
	dep, err := s.Deployments.ByName(t.Context(), pid, PreviewName(idstest.N(7)))
	if err != nil {
		t.Fatal(err)
	}
	if dep.Kind != preview || dep.ChangeID != idstest.N(7) || dep.ObservedGeneration != 2 || dep.URL != "http://x" {
		t.Fatalf("preview row: %+v", dep)
	}
	if err := s.Deployments.ForgetPreview(t.Context(), idstest.N(7)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deployments.ByName(t.Context(), pid, PreviewName(idstest.N(7))); err == nil {
		t.Fatal("a reaped preview is no environment")
	}
}

// Placement and boot receipts change together; reuse keeps the existing
// receipts and a replacement forgets resources its new boot did not create.
func TestPreviewResourceReceiptsFollowTheRecordedBoot(t *testing.T) {
	t.Parallel()
	s, db, _, pid, target := healthWorld(t)
	change := idstest.N(7)
	app := docker.PreviewChangeName(change)
	created := []deploy.Owned{{Kind: "network", ID: docker.Current.Network(docker.ChangeStackID(change))}}
	if err := s.Deployments.RecordPreview(t.Context(), pid, change, target.ID, app, 40001, "http://first", created); err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.ByName(t.Context(), pid, PreviewName(change))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Deployments.RecordPreview(t.Context(), pid, change, target.ID, app, 40001, "http://reused", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Deployments.Owned(t.Context(), dep.ID); err != nil || !reflect.DeepEqual(got, created) {
		t.Fatalf("reused receipts: %v, %v", got, err)
	}
	clear := dbtest.NextStatementFailure(db, "-- name: RecordOwned")
	err = s.Deployments.RecordPreview(t.Context(), pid, change, target.ID, app, 40001, "http://failed", []deploy.Owned{{Kind: "volume", ID: "replacement"}})
	clear()
	if err == nil {
		t.Fatal("resource recording failure was hidden")
	}
	if got, err := s.Deployments.ByID(t.Context(), dep.ID); err != nil || got.URL != "http://reused" || got.ObservedGeneration != 2 {
		t.Fatalf("failed recording changed placement: %+v, %v", got, err)
	}
	if got, err := s.Deployments.Owned(t.Context(), dep.ID); err != nil || !reflect.DeepEqual(got, created) {
		t.Fatalf("failed recording changed receipts: %v, %v", got, err)
	}
	if err := s.Deployments.RecordPreview(t.Context(), pid, change, target.ID, app, 40001, "http://replacement", []deploy.Owned{}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Deployments.Owned(t.Context(), dep.ID); err != nil || len(got) != 0 {
		t.Fatalf("old boot receipts survived replacement: %v, %v", got, err)
	}
}
