package dataprotection

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dataprotection/internal/protectionsql"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/ids"
)

// A whole-AppTown restore (stock/docs/guides/apptown-backups.md) brings a
// set of each built-in deployment's data, taken with its app stopped. The
// restored AppTown rebuilds the deployment from its record, and that
// rebuild's boot puts the set back after the needs are up and before any
// init step, part or the public app starts (deployment.RestoreFiller), so
// nothing of the app writes into, or answers from, the new and empty data.
// Only a whole set fills: a partial one, or none, fails the boot with why,
// and nothing of the app starts. The fill is the in-place restore's steps
// (plan, place, put each item back) without a safety set or stopped parts:
// the data is new and nothing of the app has run.

// Fill states.
const (
	FillWaiting = "waiting"
	FillFilled  = "filled"
	FillFailed  = "failed"
)

// RestoreFill is a built-in deployment's fill after a restore: the set
// (zero when the archive held none), its state and why.
type RestoreFill struct {
	DeploymentID ids.ID    `json:"-"`
	Set          ids.ID    `json:"set,omitzero"`
	State        string    `json:"state"`
	Detail       string    `json:"detail,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// RecordFill records that deploymentID's rebuild fills from set (zero:
// the archive holds none, and why says why), in the restored database
// before AppTown first starts; Deployment holds the deployment (filling)
// until the rebuild ends. One that will not be rebuilt is recorded failed
// at once, why saying what a person does.
func (s *core) RecordFill(ctx context.Context, deploymentID, set ids.ID, why string, rebuilt bool) error {
	state := FillWaiting
	if !rebuilt {
		state = FillFailed
	}
	return protectionsql.New(s.db.SQL()).RecordRestoreFill(ctx, protectionsql.RecordRestoreFillParams{
		DeploymentID: deploymentID, BackupID: set, State: state, Detail: why, UpdatedAt: s.now()})
}

// RelocateDiskSets moves the records of every set kept on AppTown's disk
// from the data directory from to this AppTown's: a restore onto another
// path. The same path changes nothing.
func (s *core) RelocateDiskSets(ctx context.Context, from string) (int64, error) {
	old := filepath.Join(filepath.Clean(from), "backups") + string(filepath.Separator)
	now := filepath.Join(s.dataDir, "backups") + string(filepath.Separator)
	if old == now {
		return 0, nil
	}
	return protectionsql.New(s.db.SQL()).RelocateDiskSets(ctx, protectionsql.RelocateDiskSetsParams{ToRoot: now, FromRoot: old, Disk: StoreAppTownDisk})
}

// fillShownFor is how long a deployment's card shows how its data came
// back: a day once it did, a week when it did not (time to restore by
// hand); a waiting one is shown while it waits.
var fillShownFor = map[string]time.Duration{FillFilled: 24 * time.Hour, FillFailed: 7 * 24 * time.Hour}

// FillsOf are the fills of these deployments a card shows, by deployment.
func (s *core) FillsOf(ctx context.Context, deploymentIDs []ids.ID) (map[ids.ID]RestoreFill, error) {
	rows, err := protectionsql.New(s.db.SQL()).RestoreFillsOf(ctx, deploymentIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[ids.ID]RestoreFill, len(rows))
	for _, r := range rows {
		if shown, ok := fillShownFor[r.State]; ok && s.now().Sub(r.UpdatedAt) >= shown {
			continue
		}
		out[r.DeploymentID] = RestoreFill{DeploymentID: r.DeploymentID, Set: r.BackupID, State: r.State, Detail: r.Detail, UpdatedAt: r.UpdatedAt}
	}
	return out, nil
}

// Fill is the fill a filling deployment's rebuild boots with
// (deployment.RestoreFiller). It refuses, before anything of the app
// starts, a deployment the archive held no whole set for.
func (s *core) Fill(ctx context.Context, dep *deployment.Deployment, t *deployment.Target) (func(network string) error, error) {
	row, err := protectionsql.New(s.db.SQL()).RestoreFillOf(ctx, dep.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%s waits for its data, and no fill is recorded for it", dep.Name)
	}
	if err != nil {
		return nil, err
	}
	if row.State != FillWaiting {
		return nil, fmt.Errorf("%s's fill already %s", dep.Name, row.State)
	}
	return func(string) error { return s.fill(ctx, dep, t, row) }, nil
}

func (s *core) fill(ctx context.Context, dep *deployment.Deployment, t *deployment.Target, row protectionsql.RestoreFill) error {
	if row.BackupID.IsZero() {
		return fmt.Errorf("AppTown's backup holds no set of %s's data (%s); its app is not started on empty data", dep.Name, orElse(row.Detail, "no reason recorded"))
	}
	b, err := backupRecord(protectionsql.New(s.db.SQL()).BackupByIDAnywhere(ctx, row.BackupID))
	if err != nil {
		return fmt.Errorf("reading set %s: %w", row.BackupID, err)
	}
	if b.Status != BackupOK {
		var bad []string
		for _, it := range b.Items {
			if it.Error != "" {
				bad = append(bad, it.Kind+" "+it.Name+": "+it.Error)
			}
		}
		return fmt.Errorf("set %s of %s is %s (%s); its app is not started on part of its data", b.ID, dep.Name, b.Status, strings.Join(bad, "; "))
	}
	if b.Store != StoreAppTownDisk {
		return fmt.Errorf("set %s is not on AppTown's disk, where a restore of AppTown puts it", b.ID)
	}
	p, err := project.ByIDIn(ctx, s.db, dep.ProjectID)
	if err != nil {
		return err
	}
	dk, err := s.hosts.DockerScope(ctx, t)
	if err != nil {
		return err
	}
	names, err := dep.HostNames()
	if err != nil {
		return err
	}
	for _, it := range b.Items {
		if it.Kind == "volume" && it.Error == "" {
			// The app's data volume, made before its container is, so the
			// plan finds it and its files go in first; the boot then uses it.
			volume := names.DataVolume()
			if !dk.OK(ctx, "volume", "inspect", volume) {
				if out, err := dk.Run(ctx, "volume", "create", "--label", dockerlabel.Owner+"="+names.App(), volume); err != nil {
					return fmt.Errorf("making the data volume %s: %v: %s", volume, err, strings.TrimSpace(string(out)))
				}
			}
			break
		}
	}
	held, err := s.heldOnHost(ctx, dk, p.Slug, dep)
	if errors.Is(err, ErrNothingToBackUp) && !b.holdsGood() {
		return nil // nothing kept, nothing to put back
	}
	if err != nil {
		return fmt.Errorf("planning where %s's data goes: %w", dep.Name, err)
	}
	moves, _, err := restoreMoves(&b, held.items)
	if err != nil {
		return err
	}
	for _, m := range moves {
		r := s.restoreItem(ctx, nil, dk, m, filepath.Join(b.Path, m.item.File))
		if r.Outcome == RestoreFailed {
			return fmt.Errorf("putting %s %s back from set %s: %s", r.Kind, r.Name, b.ID, r.Detail)
		}
	}
	return nil
}

// Filled settles a deployment's fill once its rebuild has ended: why ""
// when its data is back.
func (s *core) Filled(ctx context.Context, deploymentID ids.ID, why string) error {
	state, detail := FillFilled, ""
	if why != "" {
		state, detail = FillFailed, why
	}
	_, err := protectionsql.New(s.db.SQL()).SettleRestoreFill(context.WithoutCancel(ctx), protectionsql.SettleRestoreFillParams{
		State: state, Detail: detail, UpdatedAt: s.now(), DeploymentID: deploymentID, Waiting: FillWaiting})
	return err
}

func orElse(s, otherwise string) string {
	if s == "" {
		return otherwise
	}
	return s
}

var _ deployment.RestoreFiller = (*core)(nil)

// holdsGood says whether the set holds any good copy.
func (b *Backup) holdsGood() bool {
	for _, it := range b.Items {
		if it.Error == "" {
			return true
		}
	}
	return false
}
