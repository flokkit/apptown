package instancebackup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/backuparchive"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/strs"
	"github.com/flokkit/apptown/internal/version"
)

// Restoring a whole AppTown onto a fresh host (stock/docs/guides/apptown-backups.md),
// before it first starts: refuse an AppTown already there; unpack the
// archive into the data directory; check the key files and the dump came
// across unchanged; make the built-in database and load the dump, counting
// each table's rows as they go in and comparing them with the database's.
// The caller then opens the database as a normal start does (migrating it
// to this build), builds the App, and records what the restored AppTown
// must do on its first start (AfterRestore).

// RestoreRequest is a restore of an archive into an empty data directory,
// on Docker (the host's own), making the built-in database Database names
// once the archive's db.env is in place.
type RestoreRequest struct {
	DataDir string
	// LayoutVersion is the daemon's data layout, the only one it restores,
	// checked before DB effects.
	LayoutVersion int
	Archive       string
	Passphrase    string
	Say           func(string) // each step as it starts; nil says nothing
	Docker        docker.Scope
	Database      builtinDatabase
}

// Restore unpacks and loads an archive, and says what it held. Anything
// it wrote stays when it fails: a fresh host is cleared by hand.
func Restore(ctx context.Context, in RestoreRequest) (*Manifest, error) {
	say := in.Say
	if say == nil {
		say = func(string) {}
	}
	dk := in.Docker
	say("checking that no AppTown is on this host")
	if err := refuseExistingAppTown(ctx, dk, in.DataDir); err != nil {
		return nil, err
	}
	f, err := os.Open(in.Archive)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := os.MkdirAll(in.DataDir, 0o755); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(in.DataDir, ".apptown-restore-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	dump := filepath.Join(staging, dumpEntry)
	m, err := unpack(f, in.Passphrase, in.DataDir, dump, say)
	if err != nil {
		return nil, err
	}

	if err := checkRestoreLayout(in.DataDir, in.LayoutVersion); err != nil {
		return nil, err
	}
	if err := syncRestoredFiles(in.DataDir); err != nil {
		return nil, fmt.Errorf("making restored files durable before database loading: %w", err)
	}
	say("making the built-in database")
	url, err := in.Database.Ensure(ctx)
	if err != nil {
		return nil, fmt.Errorf("the built-in database: %w", err)
	}
	container, dbName, ok := in.Database.Names()
	if !ok {
		return nil, errors.New("the restored db.env names no built-in database container, volume or database")
	}
	raw, err := database.OpenRaw(url)
	if err != nil {
		return nil, err
	}
	defer raw.Close()
	tables, err := dbschema.PublicTables(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("reading the new database: %w", err)
	}
	if tables != 0 {
		return nil, fmt.Errorf("the database %s in %s already has %d tables: a restore loads only into an empty one", dbName, container, tables)
	}
	say("loading the dump")
	rows, err := loadDump(ctx, dk, container, dbName, dump)
	if err != nil {
		return nil, err
	}
	say("comparing each table's rows with the dump's")
	if err := compareRows(ctx, raw, rows); err != nil {
		return nil, err
	}
	if err := markRestoreLoaded(in.DataDir); err != nil {
		return nil, err
	}
	return m, nil
}

// unpack reads an archive from src: its manifest first (refused
// when this build cannot restore it), then the dump to dump and the data
// directory under dataDir, each written only inside it, and checks that
// the key files and the dump came across unchanged.
func unpack(src io.Reader, passphrase, dataDir, dump string, say func(string)) (*Manifest, error) {
	r, err := backuparchive.NewReader(bufio.NewReaderSize(src, 1<<20), passphrase)
	if err != nil {
		return nil, err
	}
	m, manifestRaw, err := readManifest(r)
	if err != nil {
		return nil, err
	}
	if err := beginRestoreIntent(dataDir, manifestRaw); err != nil {
		return nil, err
	}
	say(fmt.Sprintf("unpacking the archive of AppTown %s, taken %s on %s by %s", m.AppTown, m.TakenAt, m.Host, m.Build))
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	got := map[string]backuparchive.Digest{}
	for {
		hdr, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading the archive: %w", err)
		}
		switch {
		case hdr.Name == dumpEntry && hdr.Typeflag == tar.TypeReg:
			d, err := writeDump(dump, r)
			if err != nil {
				return nil, err
			}
			got[dumpEntry] = d
		case strings.HasPrefix(hdr.Name, dataPrefix):
			if err := refuseRestoreControlEntry(hdr, dataDir); err != nil {
				return nil, err
			}
			d, err := r.Extract(root, hdr, dataPrefix)
			if err != nil {
				return nil, fmt.Errorf("unpacking %s: %w", hdr.Name, err)
			}
			if hdr.Typeflag == tar.TypeReg {
				got[strings.TrimPrefix(hdr.Name, dataPrefix)] = d
			}
		default:
			return nil, fmt.Errorf("the archive holds %q, which an AppTown archive never does", hdr.Name)
		}
	}
	if err := r.Finish(); err != nil {
		return nil, err
	}
	say("checking that the key files and the dump came across unchanged")
	if err := compareKeyFiles(m, got); err != nil {
		return nil, err
	}
	return m, nil
}

// refuseExistingAppTown refuses a data directory that is not empty, and
// the containers of an AppTown already on this Docker, by name: its
// built-in database (labelled with its installation) and its deployments'
// containers (labelled with the deployment they run).
func refuseExistingAppTown(ctx context.Context, dk docker.Scope, dataDir string) error {
	pending, err := ReadPendingRestore(dataDir)
	if err != nil {
		return fmt.Errorf("cannot restart this restore: %w", err)
	}
	if pending != nil {
		return errors.New("this archive is already loaded; start AppTown to finish its pending restore instead of extracting it again")
	}

	if entries, err := os.ReadDir(dataDir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s is not empty: a restore goes into a new data directory on a host with no AppTown", dataDir)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// An AppTown's built-in database carries its installation, and its
	// deployments' containers the deployment they run.
	for _, key := range []string{dockerlabel.Instance, dockerlabel.Deployment} {
		found, err := dk.List(ctx, "ps", "-a", "--filter", "label="+key, "--format", "{{.Names}}")
		if err != nil {
			return fmt.Errorf("asking Docker whether an AppTown is here: %w", err)
		}
		if len(found) > 0 {
			sort.Strings(found)
			return fmt.Errorf("an AppTown's containers are on this host (%s): a restore goes onto a host with none", strings.Join(found, ", "))
		}
	}
	return nil
}

// readManifest reads the archive's first entry, its manifest, and
// refuses one this build cannot restore.
func readManifest(r *backuparchive.Reader) (*Manifest, []byte, error) {
	hdr, err := r.Next()
	if err != nil {
		return nil, nil, fmt.Errorf("reading the archive: %w", err)
	}
	if hdr.Name != manifestEntry {
		return nil, nil, fmt.Errorf("the archive begins with %q, not its manifest: it is not an AppTown backup", hdr.Name)
	}
	raw, err := io.ReadAll(io.LimitReader(r, 16<<20))
	if err != nil {
		return nil, nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil, fmt.Errorf("the archive's manifest does not read: %w", err)
	}
	// Absence is legacy evidence; an explicit null is malformed evidence.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, nil, err
	}
	if _, present := fields["database"]; present {
		if m.Database == nil {
			return nil, nil, fmt.Errorf("the archive's database provenance is null")
		}
		if err := m.Database.validate(); err != nil {
			return nil, nil, err
		}
		// The built-in database a restore loads into is made at the newest
		// major this build supports; it loads a dump of any server in the
		// range taken by a pg_dump no newer than it.
		if err := database.OwnServerSupported(m.Database.ServerVersionNum); err != nil {
			return nil, nil, fmt.Errorf("the archive's database cannot be restored by this build: %w", err)
		}
		if m.Database.clientMajor() > database.OwnPostgresMax {
			return nil, nil, fmt.Errorf("the archive was dumped by %s; this build restores dumps of PostgreSQL %d or older", m.Database.DumpClientVersion, database.OwnPostgresMax)
		}
	}
	if m.Format != archiveFormat || m.Version > archiveVersion {
		return nil, nil, fmt.Errorf("the archive is %s version %d; this build restores %s version %d", m.Format, m.Version, archiveFormat, archiveVersion)
	}
	if m.Schema < dbschema.Baseline() {
		return nil, nil, fmt.Errorf("the archive's database is at schema %d; this build restores schema %d or newer (1.0.0-rc.7 or newer)", m.Schema, dbschema.Baseline())
	}
	if m.Schema > dbschema.Latest() {
		return nil, nil, fmt.Errorf("the archive's database is at schema %d, newer than this build's %d: restore it with %s or newer", m.Schema, dbschema.Latest(), m.Build)
	}
	return &m, raw, nil
}

func writeDump(path string, r io.Reader) (backuparchive.Digest, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return backuparchive.Digest{}, err
	}
	d, err := backuparchive.Sum(io.TeeReader(r, f))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return d, err
}

// compareKeyFiles holds each key file and the dump to the manifest's
// digest: a missing or different one refuses, by name.
func compareKeyFiles(m *Manifest, got map[string]backuparchive.Digest) error {
	if d, ok := got[dumpEntry]; !ok || d != m.Dump {
		return errors.New("the archive's database dump is missing or not the one its manifest names")
	}
	var bad []string
	for name, want := range m.KeyFiles {
		if d, ok := got[name]; !ok || d != want {
			bad = append(bad, name)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("these key files did not come across unchanged: %s", strings.Join(bad, ", "))
	}
	return nil
}

// loadDump loads the gzipped dump into the built-in database with psql,
// stopping at the first error, and counts the rows of each table's COPY as
// they go in.
func loadDump(ctx context.Context, dk docker.Scope, container, database, path string) (map[string]int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("the dump is not gzip: %w", err)
	}
	pr, pw := io.Pipe()
	rows := map[string]int64{}
	go func() { pw.CloseWithError(countCopyRows(gz, pw, rows)) }()
	out, err := dk.Command(ctx, docker.Command{Args: []string{"exec", "-i", container, "psql", "-q", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", database},
		Stdin: pr, Stdout: io.Discard})
	_ = pr.Close()
	if err != nil {
		return nil, fmt.Errorf("loading the dump: %v: %s", err, strs.Truncate(strings.TrimSpace(string(out)), 300))
	}
	return rows, nil
}

// countCopyRows copies a plain SQL dump to w, counting the data lines of
// each COPY ... FROM stdin section by its table.
func countCopyRows(r io.Reader, w io.Writer, rows map[string]int64) error {
	br := bufio.NewReaderSize(r, 1<<20)
	table := ""
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			switch {
			case table != "" && string(line) == "\\.\n":
				table = ""
			case table != "":
				rows[table]++
			case strings.HasPrefix(string(line), "COPY ") && strings.HasSuffix(strings.TrimSpace(string(line)), "FROM stdin;"):
				fields := strings.Fields(string(line))
				table = fields[1]
				rows[table] += 0
			}
			if _, werr := w.Write(line); werr != nil {
				return werr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// compareRows holds each table's rows in the loaded database to the rows
// its COPY carried, before anything (migrations included) changes them.
func compareRows(ctx context.Context, db *sql.DB, rows map[string]int64) error {
	var differ []string
	for table, want := range rows {
		got, err := dbschema.RowsIn(ctx, db, table)
		if err != nil {
			return fmt.Errorf("counting %s: %w", table, err)
		}
		if got != want {
			differ = append(differ, fmt.Sprintf("%s holds %d rows, the dump %d", table, got, want))
		}
	}
	if len(differ) > 0 {
		sort.Strings(differ)
		return fmt.Errorf("the loaded database differs from the dump: %s", strings.Join(differ, "; "))
	}
	return nil
}

// AfterRestoreReport is what a restore left for the restored AppTown's
// first start.
type AfterRestoreReport struct {
	Relocated int64    `json:"relocated"` // sets kept on disk whose records moved to this data directory
	Held      []string `json:"held"`      // App Platform and Portainer deployments to be checked
	Filling   []string `json:"filling"`   // built-in deployments rebuilt with their newest set on disk
	NotFilled []string `json:"notFilled"` // built-in deployments whose data needs a person, with why
	NoBackup  []string `json:"noBackup"`  // built-in deployments with no whole set: not held, so a rebuild has no restored data
}

// AfterRestore records, in the restored database, what the restored
// AppTown must do when it first starts: the sets kept on disk found where
// the data directory now is, App Platform and Portainer deployments held
// until their providers are read, and each built-in deployment held until
// its rebuild puts its data back; and the receipt.
func (s *Service) AfterRestore(ctx context.Context, m *Manifest) (AfterRestoreReport, error) {
	var report AfterRestoreReport
	var err error
	if report.Relocated, err = s.deps.Protection.RelocateDiskSets(ctx, m.DataDir); err != nil {
		return report, fmt.Errorf("moving the records of the sets kept on disk: %w", err)
	}
	held, err := s.deps.Deployments.HoldAfterRestore(ctx)
	if err != nil {
		return report, fmt.Errorf("holding the App Platform and Portainer deployments: %w", err)
	}
	for _, h := range held {
		report.Held = append(report.Held, h.Name)
	}
	// Each built-in deployment comes back from its own newest whole set: on
	// AppTown's disk it came in the archive's data directory and its rebuild
	// puts it back; in the backup store, its fill refuses and nothing of the
	// app starts on empty data until it is released; with no set at all,
	// there is nothing to put back and it is not held.
	builtin, err := s.builtinDeployments(ctx)
	if err != nil {
		return report, err
	}
	var filling []ids.ID
	for _, d := range builtin {
		label := d.Project + "/" + d.Name
		set, found, err := s.deps.Protection.NewestWholeSet(ctx, d.ProjectID, d.Name)
		if err != nil {
			return report, fmt.Errorf("reading %s's backups: %w", label, err)
		}
		recorded := s.deps.Deployment.RebuildRecorded(ctx, &d.Deployment)
		if !found && recorded {
			report.NoBackup = append(report.NoBackup, label)
			continue
		}
		from := fmt.Sprintf("set %s, taken %s", set.ID, set.TakenAt)
		if !recorded {
			detail := "not rebuilt: its last release recorded no rebuild inputs; release it again"
			if found {
				detail += ", then restore " + from + " from its backups"
			}
			if err := s.deps.Protection.RecordFill(ctx, d.ID, set.ID, detail, false); err != nil {
				return report, err
			}
			report.NotFilled = append(report.NotFilled, label+": "+detail)
			continue
		}
		if err := s.deps.Protection.RecordFill(ctx, d.ID, set.ID, "", true); err != nil {
			return report, err
		}
		filling = append(filling, d.ID)
		if set.Store != dataprotection.StoreAppTownDisk {
			report.NotFilled = append(report.NotFilled, label+": its newest whole backup ("+from+") is in the backup store, so its app is not started on empty data; release it to start it, then restore that set from the store")
			continue
		}
		report.Filling = append(report.Filling, label+" from "+from)
	}
	if err := s.deps.Deployments.HoldForFill(ctx, filling); err != nil {
		return report, fmt.Errorf("holding the built-in deployments for their data: %w", err)
	}
	payload, _ := json.Marshal(map[string]any{"apptown": m.AppTown, "takenAt": m.TakenAt, "build": m.Build, "restoredBy": version.String(),
		"held": report.Held, "filling": report.Filling, "notFilled": report.NotFilled, "noBackup": report.NoBackup, "relocated": report.Relocated})
	if err := audit.Append(ctx, s.deps.DB, audit.At(s.now()).String(), "apptown-host", ids.Nil, "instance.restored", m.AppTown.String(), string(payload)); err != nil {
		return report, fmt.Errorf("recording the restore: %w", err)
	}
	return report, nil
}
