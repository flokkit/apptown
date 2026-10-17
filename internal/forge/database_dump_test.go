package forge

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
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
				if !slices.Equal(req.Args, []string{"exec", builtinDBContainer, "pg_dump", "-U", "postgres", "--no-owner", builtinDBName}) {
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
			err := dumpBuiltinDatabase(t.Context(), scope, name)
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
	go func() { done <- dumpBuiltinDatabase(ctx, scope, filepath.Join(dir, "dump.sql.gz")) }()
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
	backup := databaseBackup{db: testDB(t), docker: scope, dataDir: t.TempDir(), enabled: true}
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
