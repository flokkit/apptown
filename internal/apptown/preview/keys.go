package preview

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/preview/internal/previewsql"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// A preview's AI models key (design/subsystems/deployment.md, "Needs
// fulfilled by registering"): a preview of an app that needs llm is given
// its own key with the preview limit, which this owner records in
// preview_owned and revokes once the preview is gone from where previews
// run. A revocation that fails is retained by Deployment, in the same
// transaction that forgets the key here.

// previewKeys is the key owner for one preview, by its name. The key lives
// while AppTown keeps a record of the preview, or boots it.
func (s *Service) previewKeys(projectID ids.ID, preview string) deployment.KeyOwner {
	return deployment.KeyOwner{
		ProjectID: projectID,
		Name:      docker.PreviewLabelName(preview),
		Scope:     "llm:preview:" + preview,
		Preview:   true,
		Keys:      previewKeyRecorder{db: s.db, preview: preview, project: projectID, now: s.clock},
	}
}

type previewKeyRecorder struct {
	db      *database.DB
	preview string
	project ids.ID
	now     func() time.Time // AppTown's clock
}

// Held is the keys the preview's owned list names.
func (r previewKeyRecorder) Held(ctx context.Context) ([]integration.HeldKey, error) {
	rows, err := previewsql.New(r.db.SQL()).PreviewOwnedBy(ctx, previewsql.PreviewOwnedByParams{Preview: r.preview, Kind: deployment.KindOpenRouterKey})
	if err != nil {
		return nil, err
	}
	var out []integration.HeldKey
	for _, row := range rows {
		if k, ok := integration.ParseHeldKey(row.Ident); ok {
			out = append(out, k)
		}
	}
	return out, nil
}

func (r previewKeyRecorder) Record(ctx context.Context, k integration.HeldKey) error {
	ctx = context.WithoutCancel(ctx) // the key exists: recorded though ctx has ended
	_, err := previewsql.New(r.db.SQL()).RecordPreviewOwned(ctx, previewsql.RecordPreviewOwnedParams{ID: ids.New(clock.Or(r.now)().UTC()),
		Preview: r.preview, ProjectID: r.project, Kind: deployment.KindOpenRouterKey, Ident: k.Ident(), Name: docker.PreviewLabelName(r.preview), RecordedAt: clock.Or(r.now)().UTC()})
	return err
}

func (r previewKeyRecorder) Forget(ctx context.Context, k integration.HeldKey) error {
	ctx = context.WithoutCancel(ctx) // the key is gone: recorded though ctx has ended
	_, err := previewsql.New(r.db.SQL()).ForgetPreviewOwned(ctx, previewsql.ForgetPreviewOwnedParams{Preview: r.preview, Kind: deployment.KindOpenRouterKey, Ident: k.Ident()})
	return err
}

// ReleaseEndedPreviewKeys revokes the key of every preview that no longer
// stands where previews run and is not booting. The place previews run is
// asked, not AppTown's memory of them: however a preview ended (stopped,
// discarded, reaped for age, removed with its project, reconciled away,
// cleared when AppTown started again), it is gone from there, so its key
// goes within a pass. Each preview is asked about while its name is held,
// so a boot that finishes meanwhile is never taken for an ended preview.
// A place that cannot be asked keeps every key it might
// hold until it answers. A revoke that fails becomes a retained row naming
// the key, for a person to revoke by hand.
func (s *Service) ReleaseEndedPreviewKeys(ctx context.Context) error {
	q := previewsql.New(s.db.SQL())
	rows, err := q.AllPreviewOwned(ctx)
	if err != nil {
		return err
	}
	var failed []error
	for _, r := range rows {
		unlock, busy := s.tryHoldPreview(r.Preview)
		if busy {
			continue // booting now
		}
		stands, err := s.previewStands(ctx, r.Preview)
		if err == nil && !stands {
			err = s.releasePreviewKey(ctx, r)
		}
		unlock()
		if err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}

// previewStands says whether a preview is there: in the worker's list of
// its previews, or as a container on this AppTown's own Docker (a release
// recorded before digest bundles still boots here when a worker is
// configured). Its caller holds the preview's name, so the answer is
// current. An error is a place that could not be asked.
func (s *Service) previewStands(ctx context.Context, name string) (bool, error) {
	if s.PreviewsOnWorker() {
		names, err := s.worker.List(ctx)
		if err != nil {
			return false, &UnavailableError{Cause: err}
		}
		if slices.Contains(names, name) {
			return true, nil
		}
	}
	if s.hosts.NoLocalDocker() || s.hosts.Local().Client == nil {
		return false, nil
	}
	there, err := s.hosts.Local().Exists(ctx, "inspect", name)
	if err != nil {
		return false, fmt.Errorf("asking this AppTown's Docker about the preview %s: %w", name, err)
	}
	return there, nil
}

func (s *Service) releasePreviewKey(ctx context.Context, r previewsql.PreviewOwned) error {
	now := s.now()
	held, ok := integration.ParseHeldKey(r.Ident)
	var why error
	switch {
	case !ok:
		why = fmt.Errorf("the record %q does not name a key", r.Ident)
	case s.integrations == nil:
		why = errors.New("this AppTown has no integrations to revoke it with")
	default:
		why = s.integrations.ReleaseLLMKey(ctx, held, "llm:preview:"+r.Preview)
	}
	if why != nil && ctx.Err() != nil {
		// A revocation the caller stopped is not a key that could not be
		// revoked: nothing is retained, and the next pass asks again.
		return ctx.Err()
	}
	// What the revocation answered is recorded though the caller stops.
	ctx = context.WithoutCancel(ctx)
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		if why != nil {
			if err := s.retained.RecordPreviewKeyIn(ctx, tx, deployment.RetainedPreviewKey{ProjectID: r.ProjectID, Preview: r.Preview, Name: r.Name,
				Connector: held.Connector, Hash: held.Hash, Why: why.Error(), At: now}); err != nil {
				return err
			}
		}
		_, err := previewsql.New(tx.SQL()).ForgetPreviewOwned(ctx, previewsql.ForgetPreviewOwnedParams{Preview: r.Preview, Kind: r.Kind, Ident: r.Ident})
		return err
	})
}

// RunPreviewKeyRelease releases ended previews' keys every minute until
// ctx ends.
func (s *Service) RunPreviewKeyRelease(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if err := s.ReleaseEndedPreviewKeys(ctx); err != nil {
			log.Printf("releasing ended previews' AI models keys: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
