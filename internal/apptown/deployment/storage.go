package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

type storageRecorder struct {
	db         *database.DB
	deployment ids.ID
	now        func() time.Time
}

// StorageOf is the engine evidence owner for one deployment. Deletion authority
// remains with OwnerOf and the separately recorded owned-resource list.
func (s *Deployments) StorageOf(dep *Deployment) deploy.StorageBindings {
	return storageRecorder{db: s.db, deployment: dep.ID, now: s.clock}
}

func storageBinding(r deploymentsql.DeploymentStorageBinding) (deploy.StorageBinding, error) {
	created, err := time.Parse(time.RFC3339Nano, r.VolumeCreated)
	if err != nil {
		return deploy.StorageBinding{}, fmt.Errorf("reading storage incarnation: %w", err)
	}
	return deploy.StorageBinding{Need: r.Need, Volume: r.Volume, DataPath: r.DataPath, DeclaredImage: r.DeclaredImage, ImageID: r.ImageID, RepoDigest: r.RepoDigest, VolumeCreated: created, VolumeDriver: r.VolumeDriver, Origin: r.Origin}, nil
}

func readStorage(ctx context.Context, q *deploymentsql.Queries, deployment ids.ID, need, volume string) (deploy.StorageBinding, bool, error) {
	r, err := q.StorageBinding(ctx, deploymentsql.StorageBindingParams{DeploymentID: deployment, Need: need, Volume: volume})
	if errors.Is(err, sql.ErrNoRows) {
		return deploy.StorageBinding{}, false, nil
	}
	if err != nil {
		return deploy.StorageBinding{}, false, err
	}
	b, err := storageBinding(r)
	return b, err == nil, err
}

func (r storageRecorder) Get(ctx context.Context, need, volume string) (deploy.StorageBinding, bool, error) {
	return readStorage(ctx, deploymentsql.New(r.db.SQL()), r.deployment, need, volume)
}

func sameStorage(a, b deploy.StorageBinding) bool {
	return strings.TrimRight(a.DataPath, "/") == strings.TrimRight(b.DataPath, "/") && a.Need == b.Need && a.Volume == b.Volume && a.DeclaredImage == b.DeclaredImage && a.ImageID == b.ImageID && a.RepoDigest == b.RepoDigest && a.VolumeCreated.Equal(b.VolumeCreated) && a.VolumeDriver == b.VolumeDriver
}

func validateStorage(b deploy.StorageBinding) error {
	if b.DataPath == "" || b.Need == "" || b.Volume == "" || b.DeclaredImage == "" || b.ImageID == "" || b.VolumeCreated.IsZero() || b.VolumeDriver == "" || (b.Origin != "created" && b.Origin != "observed" && b.Origin != "restored") {
		return errors.New("storage binding requires observed engine and volume incarnation evidence")
	}
	return nil
}

func (r storageRecorder) Establish(ctx context.Context, b deploy.StorageBinding) error {
	if err := validateStorage(b); err != nil {
		return err
	}
	_, err := database.Transact(ctx, r.db, func(tx *database.Tx) (bool, error) {
		dep, err := LockDeploymentIn(ctx, tx, r.deployment)
		if err != nil {
			return false, err
		}
		return establishStorageIn(ctx, tx, dep, b, clock.Or(r.now)().UTC())
	})
	return err
}

func establishStorageIn(ctx context.Context, tx *database.Tx, dep *Deployment, b deploy.StorageBinding, at time.Time) (bool, error) {
	if err := validateStorage(b); err != nil {
		return false, err
	}
	q := deploymentsql.New(tx.SQL())
	old, found, err := readStorage(ctx, q, dep.ID, b.Need, b.Volume)
	if err != nil {
		return false, err
	}
	if found {
		if sameStorage(old, b) {
			return false, nil
		}
		return false, fmt.Errorf("the %s volume %s already has different engine or incarnation evidence", b.Need, b.Volume)
	}
	if err := q.EstablishStorageBinding(ctx, deploymentsql.EstablishStorageBindingParams{ID: ids.New(at), DeploymentID: dep.ID, Need: b.Need, Volume: b.Volume, DataPath: b.DataPath, DeclaredImage: b.DeclaredImage, ImageID: b.ImageID, RepoDigest: b.RepoDigest, VolumeCreated: b.VolumeCreated.UTC().Format(time.RFC3339Nano), VolumeDriver: b.VolumeDriver, Origin: b.Origin, RecordedAt: at}); err != nil {
		return false, err
	}
	return true, audit.At(at).RecordIn(ctx, tx, "apptown-system", dep.ProjectID, "deployment.storage-established", dep.Name, b)
}

func (r storageRecorder) Retire(ctx context.Context, need, volume string) error {
	_, err := database.Transact(ctx, r.db, func(tx *database.Tx) (bool, error) {
		dep, err := LockDeploymentIn(ctx, tx, r.deployment)
		if err != nil {
			return false, err
		}
		q := deploymentsql.New(tx.SQL())
		old, found, err := readStorage(ctx, q, r.deployment, need, volume)
		if err != nil || !found {
			return false, err
		}
		if err := q.RetireStorageBinding(ctx, deploymentsql.RetireStorageBindingParams{DeploymentID: r.deployment, Need: need, Volume: volume}); err != nil {
			return false, err
		}
		return true, audit.At(clock.Or(r.now)().UTC()).RecordIn(ctx, tx, "apptown-system", dep.ProjectID, "deployment.storage-retired", dep.Name, old)
	})
	return err
}

// retainStorageIn transfers every remaining binding before deployment deletion
// cascades its rows. Observed volumes are included without claiming ownership.
func retainStorageIn(ctx context.Context, tx *database.Tx, dep *Deployment, left []deploy.Left) ([]deploy.Left, error) {
	rows, err := deploymentsql.New(tx.SQL()).StorageBindings(ctx, dep.ID)
	if err != nil {
		return nil, err
	}
	out := append([]deploy.Left(nil), left...)
	for _, row := range rows {
		b, err := storageBinding(row)
		if err != nil {
			return nil, err
		}
		found := false
		for i := range out {
			if out[i].Handle["kind"] == "volume" && out[i].Handle["id"] == b.Volume {
				h := make(map[string]any, len(out[i].Handle)+1)
				for k, v := range out[i].Handle {
					h[k] = v
				}
				h["storageBinding"] = b
				out[i].Handle = h
				found = true
				break
			}
		}
		if !found {
			out = append(out, deploy.Left{What: "volume " + b.Volume, Data: true, Handle: map[string]any{"kind": "volume", "id": b.Volume, "name": b.Volume, "need": b.Need, "storageBinding": b}, Why: "engine evidence retained; volume deletion was not confirmed"})
		}
	}
	return out, nil
}

type storageRestoreRecorder struct {
	storageRecorder
	needs map[string]deploy.StorageCopy
}

func (r storageRestoreRecorder) Image(need, declared string, source *deploy.StorageBinding) (string, bool, error) {
	copy, ok := r.needs[need]
	if !ok {
		return "", false, fmt.Errorf("the restore has no complete copy for %s", need)
	}
	if source != nil && (source.Need != need || (copy.Volume != "" && copy.Volume != source.Volume)) {
		return "", false, fmt.Errorf("the %s copy does not name the recorded source volume", need)
	}
	if !copy.Physical {
		return declared, false, nil
	}
	if source == nil || copy.Volume == "" || copy.EngineImageID == "" {
		return "", false, fmt.Errorf("the %s physical copy lacks immutable source-engine provenance; preserve the archive and establish its source engine through verified recovery", need)
	}
	if source.DeclaredImage != declared || source.ImageID != copy.EngineImageID {
		return "", false, fmt.Errorf("the %s physical copy's engine differs from the declared or recorded source engine", need)
	}
	return source.ImageID, true, nil
}

func (r storageRestoreRecorder) Restore(ctx context.Context, source *deploy.StorageBinding, target deploy.StorageBinding) error {
	target.Origin = "restored"
	if err := validateStorage(target); err != nil {
		return err
	}
	image, exact, err := r.Image(target.Need, target.DeclaredImage, source)
	if err != nil {
		return err
	}
	if exact && (source == nil || source.DataPath == "" || strings.TrimRight(source.DataPath, "/") != strings.TrimRight(target.DataPath, "/")) {
		return fmt.Errorf("physical restore data path differs from source binding")
	}
	if exact && image != target.ImageID {
		return errors.New("physical restore changed the source engine")
	}
	_, err = database.Transact(ctx, r.db, func(tx *database.Tx) (bool, error) {
		dep, err := LockDeploymentIn(ctx, tx, r.deployment)
		if err != nil {
			return false, err
		}
		if dep.RestoreCheck != RestoreFilling {
			return false, errors.New("the deployment is not held for a restore fill")
		}
		q := deploymentsql.New(tx.SQL())
		old, found, err := readStorage(ctx, q, r.deployment, target.Need, target.Volume)
		if err != nil {
			return false, err
		}
		if (source == nil && found) || (source != nil && (!found || !sameStorage(old, *source))) {
			return false, errors.New("the restore source storage evidence changed before transition")
		}
		if err := q.RetireStorageBinding(ctx, deploymentsql.RetireStorageBindingParams{DeploymentID: r.deployment, Need: target.Need, Volume: target.Volume}); err != nil {
			return false, err
		}
		at := clock.Or(r.now)().UTC()
		target.Origin = "restored"
		if err := q.EstablishStorageBinding(ctx, deploymentsql.EstablishStorageBindingParams{ID: ids.New(at), DeploymentID: r.deployment, Need: target.Need, Volume: target.Volume, DataPath: target.DataPath, DeclaredImage: target.DeclaredImage, ImageID: target.ImageID, RepoDigest: target.RepoDigest, VolumeCreated: target.VolumeCreated.UTC().Format(time.RFC3339Nano), VolumeDriver: target.VolumeDriver, Origin: target.Origin, RecordedAt: at}); err != nil {
			return false, err
		}
		return true, audit.At(at).RecordIn(ctx, tx, "apptown-system", dep.ProjectID, "deployment.storage-restored", dep.Name, map[string]any{"source": source, "target": target})
	})
	return err
}
