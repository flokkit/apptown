package instancebackup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/backuparchive"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/version"
)

// A whole-AppTown backup (stock/docs/guides/apptown-backups.md): one
// archive, encrypted with a passphrase, holding the built-in database's
// dump, the data directory (without pgdata and cache) and a manifest. It is
// AppTown only: no app is stopped. Apps' data comes back from their own
// deployment backups, those kept on AppTown's disk being in the data
// directory (AfterRestore). The running AppTown takes it, asked through its
// control socket by `apptownd backup` (control.go), because only it can hold
// its own new work.
//
// In order: new work is held and the backup files are locked (a move to the
// store or a prune would take files the dump names); the database is
// dumped; the archive is written (manifest, dump, data directory); the
// lock and the hold are released; the archive is copied to the backup store
// when asked; the receipt is written. The dump comes before the files and
// nothing new starts in between, so no file the records name is missing
// or older than them; work already running may leave a file newer, which
// a restored AppTown treats as interrupted, as after any stop.

// ArchiveFormat names a whole-AppTown archive's manifest.
const (
	ArchiveFormat  = "apptown-backup"
	ArchiveVersion = 1
	manifestEntry  = "manifest.json"
	dumpEntry      = "database.sql.gz"
	dataPrefix     = "data/"
)

// instanceKeyFiles are the files a restore checks came across unchanged,
// with every file under ssh/: AppTown's keys and its database's bootstrap.
var keyFiles = []string{"runtime-token-key", "secrets.env", "db.env", "instance-id"}

// Manifest says what a whole-AppTown archive holds.
type Manifest struct {
	Database *DatabaseProvenance `json:"database,omitempty"`
	Format   string              `json:"format"`
	Version  int                 `json:"version"`
	// Build is the apptownd that took it; Schema its database's version.
	Build   string `json:"build"`
	Schema  int    `json:"schema"`
	AppTown ids.ID `json:"apptown"` // AppTown's own id (forge.id)
	TakenAt string `json:"takenAt"`
	Host    string `json:"host"`
	// DataDir is where the data directory was: a restore elsewhere moves the
	// records of the sets kept in it.
	DataDir  string                          `json:"dataDir"`
	Dump     backuparchive.Digest            `json:"dump"`
	KeyFiles map[string]backuparchive.Digest `json:"keyFiles"`
	// ExternalSecrets says where values AppTown reads but does not hold
	// are kept; "" when there is no such store.
	ExternalSecrets string `json:"externalSecrets,omitempty"`
	// NotIncluded says what the archive does not carry.
	NotIncluded []string `json:"notIncluded"`
	// Left are files of the data directory no archive can carry (sockets).
	Left []string `json:"left,omitempty"`
}

// Request is what a backup is asked to do: write the archive at an
// absolute path outside the data directory, encrypted with Passphrase,
// and copy it to the backup store when ToStore.
type Request struct {
	Archive    string
	Passphrase string
	ToStore    bool
	// Actor is who asked, as the host's administrator names themselves.
	Actor string
	// Say is told each step as it starts; nil says nothing.
	Say func(string)
}

// Report is what a backup did.
type Report struct {
	Archive  string        `json:"archive"`
	Bytes    int64         `json:"bytes"`
	SHA256   string        `json:"sha256"`
	StoreKey string        `json:"storeKey,omitempty"`
	Manifest Manifest      `json:"manifest"`
	Held     time.Duration `json:"held"` // how long new work was held
	Problems []string      `json:"problems,omitempty"`
}

// ErrBackupUnderWay refuses a second backup while one runs.
var ErrBackupUnderWay = errors.New("a backup of AppTown is already under way")

// Backup takes a whole-AppTown backup. An error is a backup that was not
// written; a problem after it was written (the copy to the store, the
// receipt) is in the report's problems.
func (s *Service) Backup(ctx context.Context, in Request) (Report, error) {
	say := in.Say
	if say == nil {
		say = func(string) {}
	}
	var report Report
	if s.deps.Database == nil {
		return report, errors.New("no database to back up")
	}
	// The pg_dump that will dump it, before anything is held: one older
	// than the server cannot dump it.
	provenance, err := s.provenance(ctx)
	if err != nil {
		return report, err
	}
	dataDir := s.deps.DataDir
	if err := refusePendingRestore(dataDir); err != nil {
		return report, err
	}
	archive, err := filepath.Abs(in.Archive)
	if err != nil {
		return report, err
	}
	if rel, err := filepath.Rel(dataDir, archive); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return report, fmt.Errorf("%s is inside the data directory, which the archive copies: write it elsewhere", archive)
	}
	if _, err := os.Stat(archive); err == nil {
		return report, fmt.Errorf("%s exists: name a new file", archive)
	}
	if _, err := backuparchive.NewWriter(io.Discard, in.Passphrase); err != nil {
		return report, err
	}
	if in.ToStore {
		has, err := s.deps.Protection.HasStore(ctx)
		if err != nil {
			return report, fmt.Errorf("reading the backup store: %w", err)
		}
		if !has {
			return report, errors.New("no backup store is attached: attach one, or leave out -store")
		}
	}
	release, ok := s.underWay.Hold()
	if !ok {
		return report, ErrBackupUnderWay
	}
	defer release()
	now := s.now
	actor := in.Actor
	if actor == "" {
		actor = "the host's administrator"
	}

	// 1. New work held and the backup files locked, for the dump and the copy.
	say("holding new work and dumping the database")
	held, ok := s.deps.NewWork.Hold()
	if !ok {
		return report, ErrBackupUnderWay
	}
	heldAt := now()
	defer func() { held() }()
	files, err := dataprotection.LockBackupFiles(dataprotection.DiskDirectory(dataDir), false)
	if err != nil {
		return report, fmt.Errorf("the deployment backups are being moved or pruned: %w", err)
	}
	defer files.Close()
	staging, err := os.MkdirTemp(dataDir, ".apptown-backup-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(staging)
	dump := filepath.Join(staging, dumpEntry)
	if err := s.deps.Database.Dump(ctx, dump); err != nil {
		return report, fmt.Errorf("dumping the database: %w", err)
	}

	// 2. The manifest, then the archive.
	m, err := s.manifest(ctx, dump, provenance)
	if err != nil {
		return report, err
	}
	say("writing the archive")
	partial := archive + ".partial"
	written, left, err := writeArchive(partial, in.Passphrase, &m, dump, dataDir, filepath.Base(staging))
	if err != nil {
		_ = os.Remove(partial)
		return report, fmt.Errorf("writing the archive: %w", err)
	}
	m.Left = left
	if err := os.Rename(partial, archive); err != nil {
		_ = os.Remove(partial)
		return report, err
	}
	_ = files.Close()
	held()
	report.Held = now().Sub(heldAt)
	report.Archive, report.Bytes, report.SHA256, report.Manifest = archive, written.Bytes, written.SHA256, m

	// 3. Off the host, when asked.
	if in.ToStore {
		say("copying the archive to the backup store")
		key := "apptown/" + m.AppTown.String() + "/" + filepath.Base(archive)
		if _, err := s.deps.Protection.CopyToStore(ctx, archive, key); err != nil {
			report.Problems = append(report.Problems, "the archive is written but not in the backup store: "+err.Error())
		} else {
			report.StoreKey = key
		}
	}
	kind := "instance.backup"
	if len(report.Problems) > 0 {
		kind = "instance.backup-incomplete"
	}
	payload, _ := json.Marshal(map[string]any{"archive": archive, "bytes": report.Bytes, "sha256": report.SHA256, "storeKey": report.StoreKey,
		"heldSeconds": report.Held.Seconds(), "problems": report.Problems, "asked": actor})
	if err := audit.Append(context.WithoutCancel(ctx), s.deps.DB, audit.At(now()).String(), "apptown-host", ids.Nil, kind, filepath.Base(archive), string(payload)); err != nil {
		report.Problems = append(report.Problems, "the backup's receipt could not be written: "+err.Error())
	}
	return report, nil
}

// builtinDeployments are the standing deployments on AppTown's own Docker
// host, in a stable order.
func (s *Service) builtinDeployments(ctx context.Context) ([]deployment.ProjectDeployment, error) {
	t, err := s.deps.Targets.ByName(ctx, s.deps.BuiltInTarget)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // no built-in host: no deployments on it
	}
	if err != nil {
		return nil, fmt.Errorf("reading the built-in Docker host: %w", err)
	}
	all, err := s.deps.Deployments.OnTarget(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	var out []deployment.ProjectDeployment
	for _, d := range all {
		if d.Standing() {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project+"/"+out[i].Name < out[j].Project+"/"+out[j].Name })
	return out, nil
}

// provenance observes the database server and the pg_dump that dumps it,
// and refuses a pg_dump of another major version: an older one cannot dump
// the server, and a newer one writes settings the built-in database a
// restore loads into does not know.
func (s *Service) provenance(ctx context.Context) (*DatabaseProvenance, error) {
	var server int
	if err := s.deps.DB.SQL().QueryRowContext(ctx, "SHOW server_version_num").Scan(&server); err != nil {
		return nil, fmt.Errorf("observing the backup database version: %w", err)
	}
	client, err := s.deps.Database.DumpClientVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("observing the backup dump client: %w", err)
	}
	p := &DatabaseProvenance{Engine: "postgres", ServerVersionNum: server, DumpClientVersion: client}
	if err := p.validate(); err != nil {
		return nil, err
	}
	if p.clientMajor() != server/10000 {
		return nil, fmt.Errorf("%s is not the database server's major version (PostgreSQL %d): install pg_dump %d", client, server/10000, server/10000)
	}
	return p, nil
}

func (s *Service) manifest(ctx context.Context, dump string, provenance *DatabaseProvenance) (Manifest, error) {
	now := s.now()
	m := Manifest{Format: ArchiveFormat, Version: ArchiveVersion, Build: version.String(), TakenAt: now.UTC().Format(time.RFC3339),
		DataDir: s.deps.DataDir, KeyFiles: map[string]backuparchive.Digest{},
		NotIncluded: []string{
			"the built-in database's files (pgdata): the dump holds the database",
			"the cache directory: tools and builds are fetched again",
			"apps' data: each app's data comes back from its own deployment backups; those kept on AppTown's disk are in the data directory",
			"deployments on attached Docker hosts, Portainer, App Platform and bucket websites: they stay where they are",
		}}
	m.Host, _ = os.Hostname()
	m.Database = provenance
	var err error
	if m.AppTown, err = identity.AppTownIDIn(ctx, s.deps.DB, now); err != nil {
		return m, fmt.Errorf("reading AppTown's id: %w", err)
	}
	if m.Schema, err = dbschema.Version(s.deps.DB); err != nil {
		return m, err
	}
	if m.Dump, err = backuparchive.SumFile(dump); err != nil {
		return m, err
	}
	keys, err := keyFileNames(s.deps.DataDir)
	if err != nil {
		return m, err
	}
	for _, name := range keys {
		d, err := backuparchive.SumFile(filepath.Join(s.deps.DataDir, filepath.FromSlash(name)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return m, fmt.Errorf("reading %s: %w", name, err)
		}
		m.KeyFiles[name] = d
	}
	url, project, err := s.deps.Secrets.ExternalStore()
	if err != nil {
		return m, fmt.Errorf("reading which secret store AppTown uses: %w", err)
	}
	if url != "" {
		m.ExternalSecrets = fmt.Sprintf("Infisical at %s (project %s): the values stay in Infisical; this archive holds AppTown's connection to it, not the values", url, project)
	}
	return m, nil
}

// instanceKeyFileNames are the key files and every file under ssh/,
// slash-separated, relative to the data directory.
func keyFileNames(dataDir string) ([]string, error) {
	out := append([]string{}, keyFiles...)
	err := filepath.WalkDir(filepath.Join(dataDir, "ssh"), func(p string, d os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(dataDir, p)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out, err
}

// writeInstanceArchive writes the archive at path: the manifest, the dump,
// then the data directory without pgdata, cache, the staging of deployment
// copies, the backup's own staging and the control socket. It says the
// archive's digest and size, and what of the data directory it passed over.
func writeArchive(path, passphrase string, m *Manifest, dump, dataDir, staging string) (backuparchive.Digest, []string, error) {
	if err := refusePendingRestore(dataDir); err != nil {
		return backuparchive.Digest{}, nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return backuparchive.Digest{}, nil, err
	}
	defer f.Close()
	sum := sha256.New()
	counted := &countWriter{}
	w, err := backuparchive.NewWriter(io.MultiWriter(f, sum, counted), passphrase)
	if err != nil {
		return backuparchive.Digest{}, nil, err
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return backuparchive.Digest{}, nil, err
	}
	if err := w.Bytes(manifestEntry, manifest); err != nil {
		return backuparchive.Digest{}, nil, err
	}
	if _, err := w.File(dumpEntry, dump); err != nil {
		return backuparchive.Digest{}, nil, err
	}
	skip := map[string]bool{"pgdata": true, "cache": true, "backups/.staging": true, staging: true, controlSocket: true}
	if err := w.Tree(strings.TrimSuffix(dataPrefix, "/"), dataDir, func(rel string) bool { return skip[rel] }); err != nil {
		return backuparchive.Digest{}, nil, err
	}
	if err := w.Close(); err != nil {
		return backuparchive.Digest{}, nil, err
	}
	if err := f.Sync(); err != nil {
		return backuparchive.Digest{}, nil, err
	}
	return backuparchive.Digest{SHA256: hex.EncodeToString(sum.Sum(nil)), Bytes: counted.n}, w.Left, f.Close()
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }
