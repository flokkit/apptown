package deployment

import (
	"context"
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
	if err := s.Deployments.RecordPreview(context.Background(), pid, idstest.N(7), target.ID, docker.PreviewChangeName(idstest.N(7)), 40001, "http://x"); err != nil {
		t.Fatal(err)
	}
	if err := s.Deployments.RecordPreview(context.Background(), pid, idstest.N(7), target.ID, docker.PreviewChangeName(idstest.N(7)), 40001, "http://x"); err != nil {
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
