package preview

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/worker"
)

// The boot's reply arrives after a discard whose DELETE was lost. This
// orders real Service operations deterministically; the worker completes
// its boot despite the separate removal request never arriving.
type reviewDiscardDuringBootWorker struct {
	*fakePreviewWorker
	discard   func() error
	discarded error
}

func (w *reviewDiscardDuringBootWorker) Boot(ctx context.Context, spec worker.PreviewSpec, p docker.Prepared, tree string) (worker.PreviewState, error) {
	w.discarded = w.discard()
	return w.fakePreviewWorker.Boot(ctx, spec, p, tree)
}
func (w *reviewDiscardDuringBootWorker) Remove(context.Context, string) error {
	return errors.New("review: DELETE never reached the worker")
}

func TestADiscardWinsOverABootInFlight(t *testing.T) {
	for _, run := range []bool{false, true} {
		label := "change"
		if run {
			label = "run"
		}
		t.Run(label, func(t *testing.T) {
			s, base, _, dir, project := workerPreviewWorld(t)
			w := &reviewDiscardDuringBootWorker{fakePreviewWorker: base}
			s.worker = w
			name := docker.PreviewChangeName(idstest.N(9))
			var bootErr error
			if run {
				name = docker.PreviewRunName(idstest.N(4))
				ws := filepath.Join(dir, "workspaces", "run-00000000-0004-7000-8000-000000000000")
				writePreviewBootFixture(t, ws)
				gitCommitAll(t, ws, "run work")
				w.discard = func() error { return s.DiscardRunPreview(context.Background(), idstest.N(4), []ids.ID{idstest.N(11)}) }
				_, bootErr = s.RunPreview(context.Background(), claimedPreviewStep{project, idstest.N(4), idstest.N(11)}, RunInput{Source: deploy.Source{Workspace: ws, Revision: "HEAD", Slug: "p"}})
			} else {
				writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000"))
				w.discard = func() error { return s.DiscardChangePreview(context.Background(), idstest.N(9)) }
				_, bootErr = s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: project}, false)
			}
			if bootErr != nil || w.discarded == nil {
				t.Fatalf("fixture: boot %v; lost removal %v", bootErr, w.discarded)
			}
			s.worker = base // the worker is reachable again
			if !base.holds(name) {
				t.Fatal("fixture: no completed preview")
			}
			if err := s.ReconcileWorkerPreviews(context.Background()); err != nil {
				t.Fatal(err)
			}
			if base.holds(name) {
				t.Fatal("discarded preview was recorded again by its in-flight boot and reconciliation kept it")
			}
		})
	}
}
