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
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/strs"

	"github.com/flokkit/apptown/internal/deploy/docker"
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
	output, dumpErr := scope.Command(ctx, docker.Command{Args: []string{"exec", db.Container, "pg_dump", "-U", "postgres", "--no-owner", db.Database}, Stdout: zip})
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
	dir := filepath.Join(b.dataDir, "backups")
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
		err := errors.New("db.env names no built-in database container, volume or database")
		audit.At(clock.Or(b.now)()).BestEffort(ctx, b.db, "apptown-system", ids.Nil, "db.backup-failed", name, map[string]any{"error": err.Error()})
		return err
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
