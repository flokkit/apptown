package preview

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/worker"
)

// The boot passed AppTown gate before recovery started, but reaches the
// worker after the claim. The worker's ordered fence preserves this later boot.
type reviewClaimRaceWorker struct {
	*fakePreviewWorker
	bootEntered, claimEntered, finishClaim chan struct{}
}

func (w *reviewClaimRaceWorker) Boot(ctx context.Context, spec worker.PreviewSpec, p docker.Prepared, tree string) (worker.PreviewState, error) {
	close(w.bootEntered)
	<-w.claimEntered
	return w.fakePreviewWorker.Boot(ctx, spec, p, tree)
}
func (w *reviewClaimRaceWorker) Remove(ctx context.Context, name string) error {
	if name == "apptown-preview-run-00000000-03e7-7000-8000-000000000000" {
		return worker.ErrClaimRequired
	}
	if name != "" {
		return w.fakePreviewWorker.Remove(ctx, name)
	}
	// Model claim application before the later boot, with its HTTP confirmation
	// delayed. Clearing the fake's old resources here matches worker fencing.
	if err := w.fakePreviewWorker.Remove(ctx, ""); err != nil {
		return err
	}
	close(w.claimEntered)
	<-w.finishClaim
	return nil
}
func TestARecoveryClaimKeepsALaterBootsRecord(t *testing.T) {
	s, base, _, dir, projectID := workerPreviewWorld(t)
	w := &reviewClaimRaceWorker{fakePreviewWorker: base, bootEntered: make(chan struct{}), claimEntered: make(chan struct{}), finishClaim: make(chan struct{})}
	s.worker = w
	var release sync.Once
	defer release.Do(func() { close(w.finishClaim) })
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000"))
	bootDone := make(chan error, 1)
	go func() {
		_, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, false)
		bootDone <- err
	}()
	select {
	case <-w.bootEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("boot did not enter")
	}
	claimDone := make(chan error, 1)
	go func() {
		claimDone <- s.RemoveRunForGood(context.Background(), idstest.N(999))
	}()
	select {
	case err := <-bootDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("boot did not finish")
	}
	name := "apptown-preview-change-00000000-0009-7000-8000-000000000000"
	if _, ok := s.servedRecordOf(name); !ok {
		t.Fatal("fixture: successful boot not recorded")
	}
	release.Do(func() { close(w.finishClaim) })
	select {
	case <-claimDone:
	case <-time.After(5 * time.Second):
		t.Fatal("claim did not finish")
	}
	if !base.holds(name) {
		t.Fatal("fixture: claim removed a later boot")
	}
	_, recorded := s.servedRecordOf(name)
	if err := s.ReconcileWorkerPreviews(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !recorded || !base.holds(name) {
		t.Fatalf("recovery claim forgot successful later boot: recorded=%v; retained after reconciliation=%v", recorded, base.holds(name))
	}
}
