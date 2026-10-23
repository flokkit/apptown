package apptown

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/instancebackup"
	"github.com/flokkit/apptown/internal/backuparchive"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type dumpCommand func(context.Context, string, docker.Command) ([]byte, error)

func (f dumpCommand) Run(ctx context.Context, host string, req docker.Command) ([]byte, error) {
	return f(ctx, host, req)
}

// The dump reads the database db.env's URL names: an install made before 1.0
// keeps the database forge, one made since has apptown.
func TestDatabaseDumpPublishesOnlyCompleteOutput(t *testing.T) {
	t.Parallel()
	before1 := builtinDBNames{Container: "forge-db", Volume: "forge-data-forge-db", Database: "forge"}
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "producer fails after output"}[fails], func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, "today's $HOME backup.sql.gz")
			scope := docker.Scope{Client: dumpCommand(func(_ context.Context, _ string, req docker.Command) ([]byte, error) {
				if !slices.Equal(req.Args, []string{"exec", "forge-db", "pg_dump", "-U", "postgres", "--no-owner", "forge"}) {
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
			err := dumpBuiltinDatabase(t.Context(), scope, before1, name)
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
		done <- dumpBuiltinDatabase(ctx, scope, currentBuiltinDB, filepath.Join(dir, "dump.sql.gz"))
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
		if req.Args[1] != currentBuiltinDB.Container || req.Args[len(req.Args)-1] != "apptown" {
			t.Errorf("dumped %v, not the container and database db.env names", req.Args)
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

// A whole-AppTown backup (instancebackup), as AppTown runs it.

const backupPassphrase = "a passphrase for tests"

// backupDataDirFixture is a data directory with AppTown's key files, an SSH key,
// a project's folder, and what an archive leaves out.
func backupDataDirFixture(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"runtime-token-key":            strings.Repeat("k", 32),
		"secrets.env":                  "INFISICAL_URL=https://vault.example\nINFISICAL_CLIENT_ID=c\nINFISICAL_CLIENT_SECRET=s\nINFISICAL_PROJECT_ID=p1\n",
		"db.env":                       "APPTOWN_DB_URL=postgres://postgres:pw@127.0.0.1:54321/apptown\nAPPTOWN_DB_PASSWORD=pw\nAPPTOWN_DB_CONTAINER=apptown-db\nAPPTOWN_DB_VOLUME=apptown-data-apptown-db\n",
		"instance-id":                  "abc123\n",
		"ssh/host-key":                 "ssh key",
		"workspaces/project-p/README":  "the project",
		"pgdata/PG_VERSION":            "16",
		"cache/mise/tool":              "a tool",
		"backups/.staging/take-x/file": "staged",
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// backupWorld is an AppTown on the built-in database whose dump the fake
// daemon writes, with during told while pg_dump runs.
func backupWorld(t *testing.T, during func()) *App {
	t.Helper()
	dir := t.TempDir()
	backupDataDirFixture(t, dir)
	f := newFakeDocker()
	f.command = func(ctx context.Context, host string, req docker.Command) ([]byte, error) {
		if len(req.Args) > 2 && req.Args[0] == "exec" && req.Args[2] == "pg_dump" {
			if during != nil {
				during()
			}
			_, err := io.WriteString(req.Stdout, "-- PostgreSQL database dump\nCOPY public.projects (id) FROM stdin;\n1\n\\.\n")
			return nil, err
		}
		f.command = nil
		defer func() { f.command = nil }()
		return f.Run(ctx, host, req)
	}
	a := testApp(t, Config{DataDir: dir, DockerClient: f, UsingBuiltinDB: true})
	return a
}

// A backup holds new work and the deployment backups' files from before
// the database is dumped until the archive is written, and only then: a
// job or step waits meanwhile, a move of sets to the store is refused, and
// both are free again after. The archive opens with the passphrase and
// holds the dump and the data directory; the receipt names the archive and
// never the passphrase.
//
// Kills: a dump taken with new work starting beside it, a set moved away
// between the dump and the copy, a hold left after the backup.
func TestABackupHoldsNewWorkAndTheBackupFilesAcrossTheDumpAndTheCopy(t *testing.T) {
	t.Parallel()
	var a *App
	var heldDuring, lockedDuring bool
	var second error
	a = backupWorld(t, func() {
		_, second = a.Backups.Backup(context.Background(), instancebackup.Request{Archive: filepath.Join(t.TempDir(), "second.backup"), Passphrase: backupPassphrase})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		heldDuring = errors.Is(a.newWork.Wait(ctx), context.DeadlineExceeded)
		mover, err := dataprotection.LockBackupFiles(filepath.Join(a.cfg.dataDir, "backups"), true)
		lockedDuring = err != nil
		if mover != nil {
			mover.Close()
		}
	})
	archive := filepath.Join(t.TempDir(), "apptown.backup")
	report, err := a.Backups.Backup(t.Context(), instancebackup.Request{Archive: archive, Passphrase: backupPassphrase, Actor: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(second, instancebackup.ErrBackupUnderWay) {
		t.Fatalf("a second backup while one ran: %v", second)
	}
	if !heldDuring || !lockedDuring {
		t.Fatalf("during the dump: new work held %v, backup files locked %v", heldDuring, lockedDuring)
	}
	if err := a.newWork.Wait(t.Context()); err != nil {
		t.Fatal("new work is still held after the backup")
	}
	if l, err := dataprotection.LockBackupFiles(filepath.Join(a.cfg.dataDir, "backups"), true); err != nil {
		t.Fatalf("the backup files are still locked: %v", err)
	} else {
		l.Close()
	}
	if report.Manifest.ExternalSecrets == "" || !strings.Contains(report.Manifest.ExternalSecrets, "not the values") {
		t.Fatalf("the manifest does not say the external store keeps its values: %q", report.Manifest.ExternalSecrets)
	}
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := backuparchive.NewReader(f, backupPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for {
		hdr, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
	}
	if err := r.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(names) < 3 || names[0] != "manifest.json" || names[1] != "database.sql.gz" || !slices.Contains(names, "data/workspaces/project-p/README") || slices.Contains(names, "data/pgdata/PG_VERSION") {
		t.Fatalf("the archive holds %v", names)
	}
	got := receiptsOf(t, a, ids.Nil, "instance.backup")
	if len(got) != 1 || !strings.Contains(string(got[0].Payload), report.SHA256) || strings.Contains(string(got[0].Payload), backupPassphrase) {
		t.Fatalf("the backup's receipt: %+v", got)
	}
}

// A backup refuses, before anything is held or dumped: the archive inside
// the data directory, an existing file, a short passphrase, a copy to a
// store that is not attached, a second backup while one runs, and backup
// files a move holds (with the hold released). A second backup while one
// runs is refused in TestABackupHoldsNewWorkAndTheBackupFilesAcrossTheDumpAndTheCopy.
func TestABackupRefusesBeforeItDumps(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"inside", "exists", "short", "no store", "files busy"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			dumped := false
			a := backupWorld(t, func() { dumped = true })
			in := instancebackup.Request{Archive: filepath.Join(t.TempDir(), "apptown.backup"), Passphrase: backupPassphrase}
			switch scenario {
			case "inside":
				in.Archive = filepath.Join(a.cfg.dataDir, "backups", "apptown.backup")
			case "exists":
				if err := os.WriteFile(in.Archive, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "short":
				in.Passphrase = "short"
			case "no store":
				in.ToStore = true
			case "files busy":
				mover, err := dataprotection.LockBackupFiles(filepath.Join(a.cfg.dataDir, "backups"), true)
				if err != nil {
					t.Fatal(err)
				}
				defer mover.Close()
			}
			if _, err := a.Backups.Backup(t.Context(), in); err == nil {
				t.Fatal("the backup went ahead")
			}
			if dumped {
				t.Fatal("the database was dumped")
			}
			if err := a.newWork.Wait(t.Context()); err != nil {
				t.Fatal("new work is held after a refused backup")
			}
		})
	}
}

// After a restore, the restored records say what the first start does: the
// sets kept on disk are found under this data directory, an App Platform
// deployment waits for its provider to be read, and a built-in deployment
// with a set in the archive waits for its rebuild to put its data back.
func TestAfterARestoreTheRecordsSayWhatTheFirstStartDoes(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	ctx := t.Context()
	a.SeedTargets(ctx)
	createReadTestProject(t, a, "p")
	builtin, err := a.Deployment.Targets.ByName(ctx, deployment.BuiltInTargetName)
	if err != nil {
		t.Fatal(err)
	}
	cloud, err := a.Deployment.Targets.Put(ctx, &deployment.Target{Name: "cloud", Kind: deployment.KindDOApp, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	local, err := a.Deployment.Deployments.EnsureStanding(ctx, idstest.N(1), "main", builtin.ID, deployment.Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	remote, err := a.Deployment.Deployments.EnsureStanding(ctx, idstest.N(1), "cloud", cloud.ID, deployment.Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	old := "/srv/old/data"
	set, err := dataprotection.NewBackups(a.DB).Record(ctx, &dataprotection.Backup{ProjectID: idstest.N(1), Deployment: "main", TakenAt: "2026-10-23T00:00:00Z",
		Trigger: dataprotection.TriggerAppTownBackup, Path: old + "/backups/p/main/2026-10-23", Status: dataprotection.BackupOK, Store: dataprotection.StoreAppTownDisk}, "test")
	if err != nil {
		t.Fatal(err)
	}
	m := &instancebackup.Manifest{AppTown: idstest.N(9), DataDir: old, Deployments: []instancebackup.ArchivedDeployment{{ID: local.ID, Project: "p", Deployment: "main", Set: set, Status: "ok"}}}
	report, err := a.Backups.AfterRestore(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if report.Relocated != 1 {
		t.Fatalf("relocated %d sets", report.Relocated)
	}
	sets, err := a.DataProtection.DeploymentSets(ctx, idstest.N(1), "main", 10)
	if err != nil || len(sets) != 1 || sets[0].Path != filepath.Join(a.cfg.dataDir, "backups/p/main/2026-10-23") {
		t.Fatalf("the set's record: %+v %v", sets, err)
	}
	if d, _ := a.Deployment.Deployments.ByID(ctx, remote.ID); d.RestoreCheck != deployment.RestoreUnconfirmed {
		t.Fatalf("the App Platform deployment: %q", d.RestoreCheck)
	}
	// main never released: nothing records how to rebuild it, so it is
	// not held and its fill needs a person, named.
	if d, _ := a.Deployment.Deployments.ByID(ctx, local.ID); d.RestoreCheck != "" || len(report.NotFilled) != 1 || !strings.Contains(report.NotFilled[0], "release it again") {
		t.Fatalf("the built-in deployment: %q, %v", d.RestoreCheck, report.NotFilled)
	}
	fills, err := a.DataProtection.FillsOf(ctx, []ids.ID{local.ID})
	if err != nil || fills[local.ID].State != dataprotection.FillFailed {
		t.Fatalf("its fill: %+v %v", fills, err)
	}
	if got := receiptsOf(t, a, ids.Nil, "instance.restored"); len(got) != 1 {
		t.Fatalf("restore receipts: %d", len(got))
	}
}
