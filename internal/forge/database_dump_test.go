package forge

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy/docker"
)

type dumpCommand func(context.Context, string, docker.Command) ([]byte, error)

func (f dumpCommand) Run(ctx context.Context, host string, req docker.Command) ([]byte, error) {
	return f(ctx, host, req)
}

func TestDatabaseDumpPublishesOnlyCompleteOutput(t *testing.T) {
	t.Parallel()
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "producer fails after output"}[fails], func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, "today's $HOME backup.sql.gz")
			scope := docker.Scope{Client: dumpCommand(func(_ context.Context, _ string, req docker.Command) ([]byte, error) {
				if !slices.Equal(req.Args, []string{"exec", currentBuiltinDB.Container, "pg_dump", "-U", "postgres", "--no-owner", builtinDBName}) {
					t.Fatalf("dump command: %v", req.Args)
				}
				_, err := io.WriteString(req.Stdout, "CREATE TABLE demo (id bigint);\n")
				if err != nil {
					return nil, err
				}
				if fails {
					return []byte("connection lost"), errors.New("exit 7")
				}
				return nil, nil
			})}
			err := dumpBuiltinDatabase(t.Context(), scope, currentBuiltinDB.Container, name)
			files, readErr := os.ReadDir(dir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if fails {
				if err == nil || len(files) != 0 {
					t.Fatalf("failed dump published or left scratch: %v %v", err, files)
				}
				return
			}
			if err != nil || len(files) != 1 {
				t.Fatalf("successful dump: %v %v", err, files)
			}
			f, err := os.Open(name)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			zip, err := gzip.NewReader(f)
			if err != nil {
				t.Fatal(err)
			}
			defer zip.Close()
			content, err := io.ReadAll(zip)
			if err != nil || string(content) != "CREATE TABLE demo (id bigint);\n" {
				t.Fatalf("dump bytes: %q %v", content, err)
			}
		})
	}
}

func TestDatabaseDumpCancellationLeavesNoBackup(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	scope := docker.Scope{Client: dumpCommand(func(ctx context.Context, _ string, req docker.Command) ([]byte, error) {
		if _, err := io.WriteString(req.Stdout, "partial dump"); err != nil {
			return nil, err
		}
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})}
	dir := t.TempDir()
	done := make(chan error, 1)
	go func() {
		done <- dumpBuiltinDatabase(ctx, scope, currentBuiltinDB.Container, filepath.Join(dir, "dump.sql.gz"))
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("dump did not stop after cancellation")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("cancelled dump left files: %v %v", files, err)
	}
}

func TestDatabaseBackupJobFailureDoesNotClaimTodaysDump(t *testing.T) {
	t.Parallel()
	calls := 0
	failed := errors.New("dump interrupted")
	scope := docker.Scope{Client: dumpCommand(func(_ context.Context, _ string, req docker.Command) ([]byte, error) {
		calls++
		if _, err := io.WriteString(req.Stdout, "CREATE TABLE demo (id bigint);\n"); err != nil {
			return nil, err
		}
		if calls == 1 {
			return nil, failed
		}
		return nil, nil
	})}
	backup := databaseBackup{db: testDB(t), docker: scope, dataDir: builtinDataDir(t), enabled: true}
	if receipt, err := backup.Run(t.Context(), "daily", nil); !errors.Is(err, failed) || receipt != nil {
		t.Fatalf("failed backup reported success: %v %v", receipt, err)
	}
	files, err := os.ReadDir(filepath.Join(backup.dataDir, "backups"))
	if err != nil || len(files) != 0 {
		t.Fatalf("failed backup left a published dump: %v %v", files, err)
	}
	if receipt, err := backup.Run(t.Context(), "daily", nil); err != nil || receipt["took"] == nil {
		t.Fatalf("explicit retry: %v %v", receipt, err)
	}
	if _, err := backup.Run(t.Context(), "daily", nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("daily deduplication reran a completed backup: %d calls", calls)
	}
}

// builtinDataDir is a data folder whose db.env names a built-in database,
// as apptownd's first start leaves it.
func builtinDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, _, _, err := builtinDBCredentials(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The daily dump is named apptown-<day>; the newest fourteen days are kept
// whatever they are named, so the forge-<day> dumps a release before 1.0
// wrote age out by the same rule and are never taken for newer ones.
func TestDumpRetentionKeepsTheNewestDaysUnderEitherName(t *testing.T) {
	t.Parallel()
	dir := builtinDataDir(t)
	backups := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	today := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)
	for d := 1; d <= 20; d++ {
		name := "forge-" + today.AddDate(0, 0, -d).Format("2006-01-02") + ".sql.gz"
		if err := os.WriteFile(filepath.Join(backups, name), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	scope := docker.Scope{Client: dumpCommand(func(_ context.Context, _ string, req docker.Command) ([]byte, error) {
		if req.Args[1] != currentBuiltinDB.Container {
			t.Errorf("dumped %v, not the container db.env names", req.Args)
		}
		_, err := io.WriteString(req.Stdout, "CREATE TABLE demo (id bigint);\n")
		return nil, err
	})}
	backup := databaseBackup{db: testDB(t), docker: scope, dataDir: dir, enabled: true, now: func() time.Time { return today }}
	if _, err := backup.Run(t.Context(), "daily", nil); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(backups)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, e := range entries {
		kept = append(kept, e.Name())
	}
	sort.Strings(kept)
	want := []string{"apptown-2026-11-20.sql.gz"}
	for d := 1; d <= 13; d++ {
		want = append(want, "forge-"+today.AddDate(0, 0, -d).Format("2006-01-02")+".sql.gz")
	}
	sort.Strings(want)
	if !slices.Equal(kept, want) {
		t.Fatalf("kept %v, want %v", kept, want)
	}
}
