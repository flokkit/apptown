package apptown

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/containerresult"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/strs"
	"github.com/flokkit/apptown/internal/toolversions"
)

// databaseDump dumps AppTown's database the one way it is dumped, for the
// daily backup of the built-in database and for a whole-AppTown backup
// (instancebackup.Database): pg_dump --no-owner --no-privileges, plain SQL,
// gzipped, run in a throwaway container of the postgres line of the tool
// versions that matches the server's major version. The connection is
// libpq's environment, given to docker by name and never in argv. The
// built-in database (URL empty) is reached inside its own container's
// network; a -db-url database (URL) over the daemon's default network.
type databaseDump struct {
	DB      *database.DB
	Docker  docker.Scope
	Tools   toolversions.Source
	DataDir string
	// URL is the -db-url database; empty dumps the built-in one db.env
	// names under DataDir.
	URL string
}

// dumpTool is the tool versions tool whose run image dumps the database.
const dumpTool = "postgres"

// client is the image that dumps the server, and the connection and
// docker options it is given.
func (d databaseDump) client(ctx context.Context) (image string, env, options []string, err error) {
	var server int
	if err := d.DB.SQL().QueryRowContext(ctx, "SHOW server_version_num").Scan(&server); err != nil {
		return "", nil, nil, fmt.Errorf("reading the database server's version: %w", err)
	}
	major := strconv.Itoa(server / 10000)
	image, ok := d.Tools.Current().RunImage(dumpTool, major)
	if !ok {
		return "", nil, nil, fmt.Errorf("PostgreSQL %s has no pinned pg_dump in this AppTown's tool versions; a backup dumps with the server's own major version", major)
	}
	if d.URL != "" {
		if env, err = libpqEnv(d.URL); err != nil {
			return "", nil, nil, err
		}
		if loopback(d.URL) {
			return "", nil, nil, errors.New("the -db-url names a loopback address, which inside the backup's pg_dump container is the container itself; name an address that container can reach")
		}
		return image, env, nil, nil
	}
	names, raw, ok := readBuiltinDB(d.DataDir)
	if !ok {
		return "", nil, nil, errors.New("db.env names no built-in database container, volume or database")
	}
	if env, err = libpqEnv(raw); err != nil {
		return "", nil, nil, err
	}
	// Inside the database container's own network namespace the server
	// is on 127.0.0.1:5432, wherever apptownd itself runs.
	for i, kv := range env {
		switch {
		case strings.HasPrefix(kv, "PGHOST="):
			env[i] = "PGHOST=127.0.0.1"
		case strings.HasPrefix(kv, "PGPORT="):
			env[i] = "PGPORT=5432"
		}
	}
	return image, env, []string{"--network", "container:" + names.Container}, nil
}

// loopback says whether a postgres:// URL's host is this machine's
// loopback, by name or address.
func loopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Dump writes a whole gzipped dump at path, or nothing: it is published
// only after pg_dump, its trailer, compression and the file writes all
// succeeded. A producer failure never becomes a backup merely because gzip
// exited successfully.
func (d databaseDump) Dump(ctx context.Context, path string) error {
	image, env, options, err := d.client(ctx)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".apptown-dump-*")
	if err != nil {
		return err
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	zip := gzip.NewWriter(file)
	// RunStep holds the dump to its trailer: a dump whose end did not
	// arrive is a failed backup.
	output, dumpErr := d.Docker.RunStep(ctx, docker.Step{Image: image, Options: append([]string{"--entrypoint", ""}, options...), Env: env,
		Argv: []string{"pg_dump", "--no-owner", "--no-privileges"}, Stdout: zip})
	zipErr := zip.Close()
	if dumpErr != nil {
		return fmt.Errorf("pg_dump: %w: %s", dumpErr, strs.Truncate(string(output), 300))
	}
	if zipErr != nil {
		return fmt.Errorf("compressing database dump: %w", zipErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// DumpClientVersion is the pg_dump that Dump runs, as `pg_dump --version`
// says it in a throwaway container of the same image: read from its answer
// file, so a first pull's messages never reach it.
func (d databaseDump) DumpClientVersion(ctx context.Context) (string, error) {
	image, _, _, err := d.client(ctx)
	if err != nil {
		return "", err
	}
	raw, err := d.Docker.Result(ctx, containerresult.Spec{Image: image, Script: "pg_dump --version"})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// databaseBackup owns the instance backup effect, independently of job delivery.
// Its settings are captured during construction; no worker retains App.
type databaseBackup struct {
	db      *database.DB
	dump    databaseDump
	dataDir string
	enabled bool
	now     func() time.Time // AppTown's clock, which dates the day's dump
}

func (b databaseBackup) Run(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
	if !b.enabled {
		return map[string]any{"skipped": "AppTown's database is not the built-in one"}, nil
	}
	if err := b.take(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"took": "today's dump, unless it already existed"}, nil
}

const builtinDBBackupKeep = 14

// take dumps the built-in database into <dataDir>/backups as a
// gzipped SQL file, keeps the last fourteen, and audits the dump. Run by
// the scheduler once a day (design/subsystems/postgres-migration.md §3.8). A restore
// is `gunzip -c file | docker exec -i <APPTOWN_DB_CONTAINER> psql -U postgres <database>`
// into an empty database (the one APPTOWN_DB_URL names).
func (b databaseBackup) take(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if !b.enabled {
		return nil
	}
	dir := dataprotection.DiskDirectory(b.dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("database backup: %v", err)
		return err
	}
	name := filepath.Join(dir, "apptown-"+clock.Or(b.now)().UTC().Format("2006-01-02")+".sql.gz")
	if _, err := os.Stat(name); err == nil {
		return nil // today's is done
	}
	if err := b.dump.Dump(ctx, name); err != nil {
		audit.At(clock.Or(b.now)()).BestEffort(ctx, b.db, "apptown-system", ids.Nil, "db.backup-failed", name, map[string]any{"error": err.Error()})
		return err
	}
	fi, err := os.Stat(name)
	if err != nil {
		return fmt.Errorf("database backup %s: reading what was written: %w", name, err)
	}
	// The backup's record is its evidence: one not recorded fails the job,
	// though the file stays (it is a good backup).
	if err := audit.At(clock.Or(b.now)()).Record(context.WithoutCancel(ctx), b.db, "apptown-system", ids.Nil, "db.backup", name, map[string]any{"bytes": fi.Size(), "keep": builtinDBBackupKeep}); err != nil {
		return fmt.Errorf("database backup %s was written, but its record was not: %w", name, err)
	}
	// Keep the newest N, by the day each is of: a release before 1.0 named
	// them forge-<day>, and those age out under the same rule.
	var entries []string
	for _, p := range []string{"apptown-", "forge-"} {
		found, _ := filepath.Glob(filepath.Join(dir, p+"*.sql.gz"))
		entries = append(entries, found...)
	}
	day := func(path string) string {
		base := filepath.Base(path)
		return strings.TrimPrefix(strings.TrimPrefix(base, "apptown-"), "forge-")
	}
	sort.Slice(entries, func(i, j int) bool { return day(entries[i]) < day(entries[j]) })
	if len(entries) > builtinDBBackupKeep {
		for _, old := range entries[:len(entries)-builtinDBBackupKeep] {
			_ = os.Remove(old)
		}
	}
	return nil
}

// libpqEnv is a postgres:// URL as libpq's environment variables. A URL
// part it does not know is refused rather than dropped.
func libpqEnv(raw string) ([]string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return nil, errors.New("the -db-url is not a postgres:// URL")
	}
	env := []string{"PGHOST=" + u.Hostname(), "PGDATABASE=" + strings.TrimPrefix(u.Path, "/")}
	if p := u.Port(); p != "" {
		env = append(env, "PGPORT="+p)
	}
	if u.User != nil {
		env = append(env, "PGUSER="+u.User.Username())
		if pw, ok := u.User.Password(); ok {
			env = append(env, "PGPASSWORD="+pw)
		}
	}
	for k, v := range u.Query() {
		switch k {
		case "sslmode":
			env = append(env, "PGSSLMODE="+v[0])
		case "sslrootcert":
			// The file is on this host or in apptownd's container, never in
			// the dump's container, which mounts nothing of either.
			return nil, errors.New("the -db-url names sslrootcert, a certificate file the backup's pg_dump container cannot read; a whole-AppTown backup cannot dump this database")
		default:
			return nil, fmt.Errorf("the -db-url's %q setting is not one a backup passes to pg_dump", k)
		}
	}
	return env, nil
}
