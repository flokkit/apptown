package forge

import (
	"compress/gzip"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/strs"

	"flokkit.io/forge/internal/deploy/docker"
)

// dumpBuiltinDatabase publishes a complete compressed dump only after pg_dump,
// compression and the file writes all succeed. A producer failure must never
// become today's successful backup merely because gzip exited successfully.
func dumpBuiltinDatabase(ctx context.Context, scope docker.Scope, name string) error {
	file, err := os.CreateTemp(filepath.Dir(name), ".forge-dump-*")
	if err != nil {
		return err
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	zip := gzip.NewWriter(file)
	output, dumpErr := scope.Command(ctx, docker.Command{Args: []string{"exec", builtinDBContainer, "pg_dump", "-U", "postgres", "--no-owner", builtinDBName}, Stdout: zip})
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
	now     func() time.Time // the forge's clock, which dates the day's dump
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
// is `gunzip -c file | docker exec -i forge-db psql -U postgres forge`
// into an empty database, rehearsed once before old SQLite backups go.
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
	name := filepath.Join(dir, "forge-"+clock.Or(b.now)().UTC().Format("2006-01-02")+".sql.gz")
	if _, err := os.Stat(name); err == nil {
		return nil // today's is done
	}
	if err := dumpBuiltinDatabase(ctx, b.docker, name); err != nil {
		audit.At(clock.Or(b.now)()).BestEffort(ctx, b.db, "apptown-system", ids.Nil, "db.backup-failed", name, map[string]any{"error": err.Error()})
		return err
	}
	fi, _ := os.Stat(name)
	var size int64
	if fi != nil {
		size = fi.Size()
	}
	audit.At(clock.Or(b.now)()).BestEffort(ctx, b.db, "apptown-system", ids.Nil, "db.backup", name, map[string]any{"bytes": size, "keep": builtinDBBackupKeep})
	// Keep the newest N.
	entries, _ := filepath.Glob(filepath.Join(dir, "forge-*.sql.gz"))
	if len(entries) > builtinDBBackupKeep {
		for _, old := range entries[:len(entries)-builtinDBBackupKeep] {
			_ = os.Remove(old)
		}
	}
	return nil
}
