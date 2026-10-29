package apptown

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/strs"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"net/url"
)

// dumpBuiltinDatabase publishes a complete compressed dump only after pg_dump,
// compression and the file writes all succeed. A producer failure must never
// become today's successful backup merely because gzip exited successfully.
func dumpBuiltinDatabase(ctx context.Context, scope docker.Scope, db builtinDBNames, name string) error {
	file, err := os.CreateTemp(filepath.Dir(name), ".apptown-dump-*")
	if err != nil {
		return err
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	zip := gzip.NewWriter(file)
	// RunStep holds the dump to its trailer: a dump whose end did not
	// arrive is a failed backup, not today's.
	output, dumpErr := scope.RunStep(ctx, docker.Step{Exec: db.Container, Argv: []string{"pg_dump", "-U", "postgres", "--no-owner", "--no-privileges", db.Database}, Stdout: zip})
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
	if err := os.Rename(file.Name(), name); err != nil {
		return err
	}
	return nil
}

// databaseBackup owns the instance backup effect, independently of job delivery.
// Its settings are captured during construction; no worker retains App.
type databaseBackup struct {
	db      *database.DB
	docker  docker.Scope
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
// into an empty database (the one APPTOWN_DB_URL names), rehearsed once
// before old SQLite backups go.
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
	names, ok := readBuiltinDBNames(b.dataDir)
	if !ok {
		namesErr := errors.New("db.env names no built-in database container, volume or database")
		audit.At(clock.Or(b.now)()).BestEffort(ctx, b.db, "apptown-system", ids.Nil, "db.backup-failed", name, map[string]any{"error": namesErr.Error()})
		return namesErr
	}
	if err := dumpBuiltinDatabase(ctx, b.docker, names, name); err != nil {
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

// ExternalDatabase is the -db-url database AppTown runs on, as a
// whole-AppTown backup dumps it (instancebackup.Database): with the host's
// own pg_dump, which must be at least the server's version (the backup
// checks), the same dump as the built-in database's (no owners, no
// privileges, plain and gzipped), so it loads into the built-in database a
// restore makes. The connection travels in the environment, never in argv.
type ExternalDatabase struct {
	URL string
	// PGDump is the pg_dump to run: "pg_dump", from the host's PATH.
	PGDump string
	Runner execution.Runner
}

// dumpTimeout bounds one dump of AppTown's database.
const dumpTimeout = time.Hour

// Dump writes a whole gzipped dump at path, or nothing.
func (e ExternalDatabase) Dump(ctx context.Context, path string) error {
	env, err := libpqEnv(e.URL)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".apptown-dump-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	_ = file.Close()
	defer os.Remove(tmp)
	out := e.Runner.Run(ctx, execution.Local, execution.Request{
		Argv: []string{e.PGDump, "--no-owner", "--no-privileges", "--compress=6", "--file=" + tmp}, Env: env, Timeout: dumpTimeout})
	if !out.Exited0() {
		return fmt.Errorf("pg_dump: %s", outcomeText(out))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// DumpClientVersion is the host's pg_dump, as `pg_dump --version` says it.
func (e ExternalDatabase) DumpClientVersion(ctx context.Context) (string, error) {
	out := e.Runner.Run(ctx, execution.Local, execution.Request{Argv: []string{e.PGDump, "--version"}, Timeout: time.Minute})
	if !out.Exited0() {
		return "", fmt.Errorf("pg_dump --version: %s (a backup of a -db-url database needs the host's pg_dump)", outcomeText(out))
	}
	return strings.TrimSpace(string(out.Output)), nil
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
			env = append(env, "PGSSLROOTCERT="+v[0])
		default:
			return nil, fmt.Errorf("the -db-url's %q setting is not one a backup passes to pg_dump", k)
		}
	}
	return env, nil
}

// outcomeText says how a run that did not exit 0 ended.
func outcomeText(out execution.Outcome) string {
	said := strs.Truncate(strings.TrimSpace(string(out.Output)), 300)
	switch {
	case out.RunErr != nil:
		return out.RunErr.Error() + ": " + said
	case out.Termination != execution.Exited:
		return string(out.Termination) + ": " + said
	}
	return fmt.Sprintf("exit %d: %s", out.ExitCode, said)
}
