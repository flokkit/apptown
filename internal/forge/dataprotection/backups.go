package dataprotection

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/dataprotection/internal/protectionsql"
	"flokkit.io/forge/internal/forge/identity"
)

// Backups (design/subsystems/deployment.md §8.3a as built): a set is one
// directory per take with a manifest, recorded as a row with its items and
// its state. The forge keeps the newest BackupKeep per deployment. Taking a
// set runs at the target; this is the ledger of what was taken.

const BackupKeep = 7

type Backup struct {
	ID ids.ID `json:"id"`
	// Forge is the AppTown that took the set, named in its manifest so a set
	// read from a bucket says whose it is; records leave it unset.
	Forge      ids.ID `json:"apptown,omitzero"`
	ProjectID  ids.ID `json:"projectId"`
	Deployment string `json:"deployment"`
	TakenAt    string `json:"takenAt"`
	Trigger    string `json:"trigger"`
	Actor      string `json:"actor"`
	// ActorPrincipal is the taker's principal; zero for the forge, or one
	// not resolved to a person.
	ActorPrincipal ids.ID       `json:"actorPrincipal,omitzero"`
	Path           string       `json:"path"`
	Bytes          int64        `json:"bytes"`
	Items          []BackupItem `json:"items"`
	Status         BackupState  `json:"status"`
	Error          string       `json:"error,omitempty"`
	// Store is where the set's files are: StoreForgeDisk (Path is a
	// directory on the forge's disk) or StoreBackupStore (Path is the
	// object prefix in the attached backup store).
	Store string `json:"store"`
	// DataClass is the data class of the deployment the set was taken of;
	// "" on sets recorded before sets carried it.
	DataClass string `json:"dataClass,omitempty"`
	// StoreEndpoint and StoreBucket are where a set in the backup store was
	// written; it is read and pruned there and nowhere else.
	StoreEndpoint string `json:"storeEndpoint,omitempty"`
	StoreBucket   string `json:"storeBucket,omitempty"`
}

// Where a set's files are.
const (
	StoreForgeDisk   = "apptown-disk"
	StoreBackupStore = "store"
)

type BackupItem struct {
	Kind    string `json:"kind"` // volume | need
	Name    string `json:"name"` // the volume, or the need (postgres)
	File    string `json:"file"` // the file inside the set's directory
	Bytes   int64  `json:"bytes"`
	SHA256  string `json:"sha256"`
	Error   string `json:"error,omitempty"`
	How     string `json:"how"`               // the command or "tar of <path>"
	Restore string `json:"restore,omitempty"` // the command that puts it back (needs)
	// Volume is the recorded volume a take-down's final copy was taken
	// from: the copy certifies that volume and no other.
	Volume string `json:"volume,omitempty"`
	// Copy is how the item was copied; absent on older sets, which do not
	// say.
	Copy CopyMethod `json:"copy,omitempty"`
	// RestoreStopped and Verify are the need's declared restore mode and
	// check when the item was taken: the set says how to put it back.
	RestoreStopped bool   `json:"restoreStopped,omitempty"`
	Verify         string `json:"verify,omitempty"`
	// Caveat is what this copy does not hold, as the need's kind declares.
	Caveat string `json:"caveat,omitempty"`
	// Format is the copy's declared representation (pg-custom), for an item
	// copied by its kind's portable copy: it is filled only into a kind
	// that reads the same format.
	Format string `json:"format,omitempty"`
	// Instance and Database are the managed instance and the database in
	// it the copy was taken from: the copy certifies that database and no
	// other.
	Instance string `json:"instance,omitempty"`
	Database string `json:"database,omitempty"`
	// Bucket is the deployment's object storage bucket the copy was taken
	// from (App Platform): the copy certifies that bucket and no other.
	Bucket string `json:"bucket,omitempty"`
	// AsUser and Client are whose connection the copy was taken with and
	// the image its commands ran in; a drill and a start fill it the same
	// way.
	AsUser string `json:"asUser,omitempty"`
	Client string `json:"client,omitempty"`
}

// ProjectBackup is a set with its project's name, for the fleet list.
type ProjectBackup struct {
	Backup
	Project string `json:"project"`
}

type Backups struct {
	db    *database.DB
	clock func() time.Time // the forge's clock, which stamps a set's receipt; nil is the real one
}

func (s *Backups) now() time.Time { return clock.Or(s.clock)().UTC() }

func NewBackups(db *database.DB) *Backups { return &Backups{db: db} }

// Record writes a taken set and its receipt together and returns its id.
func (s *Backups) Record(ctx context.Context, b *Backup, who string) (ids.ID, error) {
	ctx = context.WithoutCancel(ctx) // the set is taken: its record is written though ctx has ended
	if _, err := b.Status.Value(); err != nil {
		return ids.Nil, err
	}
	items, _ := json.Marshal(b.Items)
	if b.Items == nil {
		items = []byte("[]")
	}
	taken, err := time.Parse(time.RFC3339Nano, b.TakenAt)
	if err != nil {
		return ids.Nil, fmt.Errorf("reading the set's taken time %q: %w", b.TakenAt, err)
	}
	var id ids.ID
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		taker, err := identity.ActorIn(ctx, tx, b.Actor)
		if err != nil {
			return err
		}
		r, err := protectionsql.New(tx.SQL()).InsertBackup(ctx, protectionsql.InsertBackupParams{ID: ids.New(taken),
			ProjectID:      b.ProjectID,
			Deployment:     b.Deployment,
			TakenAt:        b.TakenAt,
			Trigger:        b.Trigger,
			Actor:          b.Actor,
			ActorPrincipal: taker.Principal,
			ActorVia:       taker.Via,
			Path:           b.Path,
			Bytes:          b.Bytes,
			ItemsJson:      items,
			Status:         b.Status.String(),
			Error:          b.Error,
			Store:          b.storeName(),
			DataClass:      b.DataClass,
			StoreEndpoint:  b.StoreEndpoint,
			StoreBucket:    b.StoreBucket,
		})
		id = r.ID
		if err != nil {
			return err
		}
		if who == "" {
			who = "apptown-system"
		}
		return audit.At(s.now()).RecordIn(ctx, tx, who, b.ProjectID, "deployment.backup", b.Deployment,
			map[string]any{"backup": id, "trigger": b.Trigger, "status": b.Status, "bytes": b.Bytes, "items": len(b.Items), "error": b.Error})
	})
	return id, err
}

// Note adds to a recorded set's error what was found after it was
// recorded (its manifest could not be written).
func (s *Backups) Note(ctx context.Context, id ids.ID, note string) error {
	n, err := protectionsql.New(s.db.SQL()).NoteBackupError(ctx, protectionsql.NoteBackupErrorParams{ID: id, Note: note})
	if err == nil && n != 1 {
		err = fmt.Errorf("set %d is not recorded", id)
	}
	return err
}

func backupRecord(r protectionsql.Backup, err error) (Backup, error) {
	if err != nil {
		return Backup{}, err
	}
	b := Backup{ID: r.ID, ProjectID: r.ProjectID, Deployment: r.Deployment, TakenAt: r.TakenAt.UTC().Format(time.RFC3339Nano), Trigger: r.Trigger, Actor: r.Actor, ActorPrincipal: r.ActorPrincipal, Path: r.Path, Bytes: r.Bytes, Error: r.Error,
		Store: r.Store, DataClass: r.DataClass, StoreEndpoint: r.StoreEndpoint, StoreBucket: r.StoreBucket}
	if err := b.Status.Scan(r.Status); err != nil {
		return Backup{}, err
	}
	// A set's items are what a restore puts back: unreadable items are an
	// error, never an empty set (which would restore nothing and say so).
	if len(bytes.TrimSpace(r.ItemsJson)) > 0 {
		if err := json.Unmarshal(r.ItemsJson, &b.Items); err != nil {
			return Backup{}, fmt.Errorf("set %d's items could not be read: %w", r.ID, err)
		}
	}
	if b.Items == nil {
		b.Items = []BackupItem{}
	}
	return b, nil
}

// ForDeployment is a deployment's sets, newest first.
func (s *Backups) ForDeployment(ctx context.Context, projectID ids.ID, deployment string, limit int) ([]Backup, error) {
	rows, err := protectionsql.New(s.db.SQL()).BackupsForDeployment(ctx, protectionsql.BackupsForDeploymentParams{ProjectID: projectID, Deployment: deployment, RowLimit: int64(limit)})
	out := []Backup{}
	if err != nil {
		return []Backup{}, err
	}
	for _, r := range rows {
		b, err := backupRecord(r, nil)
		if err != nil {
			return []Backup{}, err
		}
		out = append(out, b)
	}
	return out, nil
}

// All is every set the forge holds, newest first, with the project's name.
func (s *Backups) All(ctx context.Context, limit int) ([]ProjectBackup, error) {
	rows, err := protectionsql.New(s.db.SQL()).ProjectBackups(ctx, int64(limit))
	out := []ProjectBackup{}
	if err != nil {
		return []ProjectBackup{}, err
	}
	for _, r := range rows {
		b, err := backupRecord(r.Backup, nil)
		if err != nil {
			return []ProjectBackup{}, err
		}
		out = append(out, ProjectBackup{Backup: b, Project: r.Slug})
	}
	return out, nil
}

// ByID is one of a deployment's sets.
func (s *Backups) ByID(ctx context.Context, projectID ids.ID, deployment string, id ids.ID) (*Backup, error) {
	b, err := backupRecord(protectionsql.New(s.db.SQL()).BackupByID(ctx, protectionsql.BackupByIDParams{ProjectID: projectID, Deployment: deployment, ID: id}))
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// InProject is one of a project's sets, whichever deployment it was taken
// of: a restore can put a taken-down deployment's final set into another.
func (s *Backups) InProject(ctx context.Context, projectID ids.ID, id ids.ID) (*Backup, error) {
	b, err := backupRecord(protectionsql.New(s.db.SQL()).BackupInProject(ctx, protectionsql.BackupInProjectParams{ProjectID: projectID, ID: id}))
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// storeName is where the set's files are; a set that does not say is on
// the forge's disk, as every set was before the backup store.
func (b *Backup) storeName() string {
	if b.Store == "" {
		return StoreForgeDisk
	}
	return b.Store
}

// TriggerPreRestore names the safety set a restore takes first.
const TriggerPreRestore = "pre-restore"

// SafetyCopy reports whether the set was taken to protect a restore. A
// safety copy must not prune the older set the restore is about to read;
// ordinary sets resume retention after it.
func (b *Backup) SafetyCopy() bool { return b.Trigger == TriggerPreRestore }

// GoodCopy is the set's copy of an item that was taken without error, or
// nil when the set holds none: only a good copy is ever restored.
func (b *Backup) GoodCopy(kind, name string) *BackupItem {
	for i := range b.Items {
		if b.Items[i].Kind == kind && b.Items[i].Name == name && b.Items[i].Error == "" {
			return &b.Items[i]
		}
	}
	return nil
}

// DeploymentSets are a deployment's backup sets, newest first.
func (s *Service) DeploymentSets(ctx context.Context, projectID ids.ID, deployment string, limit int) ([]Backup, error) {
	return s.backups.ForDeployment(ctx, projectID, deployment, limit)
}

// AllSets are every set the forge holds, newest first, with each one's
// project.
func (s *Service) AllSets(ctx context.Context, limit int) ([]ProjectBackup, error) {
	return s.backups.All(ctx, limit)
}
