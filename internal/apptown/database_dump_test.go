package apptown

import (
	"compress/gzip"
	"context"
	"encoding/json"
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
	"github.com/flokkit/apptown/internal/containerresult"
	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/toolversions"
	"github.com/flokkit/apptown/stock"
)

type dumpCommand func(context.Context, string, docker.Command) ([]byte, error)

// Run answers req as a daemon whose container runs the dump's frame: what
// it sees is the command inside.
func (f dumpCommand) Run(ctx context.Context, host string, req docker.Command) ([]byte, error) {
	req.Args = containerresulttest.UnframeArgs(req.Args)
	return f(ctx, host, req)
}

// dumpTools is the stock tool versions, whose postgres 16 line pins the
// image a dump runs in; the test database is PostgreSQL 16.
func dumpTools(t *testing.T) *toolversions.Versions {
	t.Helper()
	snap, err := toolversions.NewSnapshot("stock", stock.ToolVersions)
	if err != nil {
		t.Fatal(err)
	}
	return toolversions.New(snap)
}

// dumpImage is the image a dump of a PostgreSQL 16 server runs in.
func dumpImage(t *testing.T) string {
	t.Helper()
	image, ok := dumpTools(t).Current().RunImage("postgres", "16")
	if !ok || !strings.Contains(image, "@sha256:") {
		t.Fatalf("no pinned postgres 16 run image: %q", image)
	}
	return image
}

// builtinDump is the dump of the built-in database db.env under dataDir
// names, on scope.
func builtinDump(t *testing.T, scope docker.Scope, dataDir string) databaseDump {
	return databaseDump{DB: testDB(t), Docker: scope, Tools: dumpTools(t), DataDir: dataDir}
}

// dataDirWithDB is a data folder whose db.env names container and database.
func dataDirWithDB(t *testing.T, container, database string) string {
	t.Helper()
	dir := t.TempDir()
	body := "APPTOWN_DB_URL=postgres://postgres:pw@127.0.0.1:54321/" + database + "\nAPPTOWN_DB_PASSWORD=pw\nAPPTOWN_DB_CONTAINER=" + container + "\nAPPTOWN_DB_VOLUME=" + container + "-data\n"
	if err := os.WriteFile(filepath.Join(dir, "db.env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The built-in database is dumped by a throwaway container of the pinned
// postgres image that joins the database container's network, never by
// exec in the database container, reading the database db.env's URL names
// (an install made before 1.0 keeps the database forge, one made since has
// apptown), with the connection in the environment by name: the password
// is never in docker's argv.
//
// Kills: the old exec of the database container's own pg_dump; the
// password in argv; output published from a failed producer.
func TestDatabaseDumpPublishesOnlyCompleteOutput(t *testing.T) {
	t.Parallel()
	image := dumpImage(t)
	before1 := builtinDBNames{Container: "forge-db", Volume: "forge-data-forge-db", Database: "forge"}
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "producer fails after output"}[fails], func(t *testing.T) {
			dir := t.TempDir()
			data := dataDirWithDB(t, before1.Container, before1.Database)
			name := filepath.Join(dir, "today's $HOME backup.sql.gz")
			scope := docker.Scope{Client: dumpCommand(func(_ context.Context, _ string, req docker.Command) ([]byte, error) {
				want := []string{"run", "--rm", "--entrypoint", "", "--network", "container:forge-db"}
				if !slices.Equal(req.Args[:len(want)], want) || !slices.Contains(req.Args, image) || req.Args[len(req.Args)-1] != "'pg_dump' '--no-owner' '--no-privileges'" {
					t.Fatalf("dump command: %v", req.Args)
				}
				if strings.Contains(strings.Join(req.Args, " "), "pw") || !slices.Contains(req.Env, "PGPASSWORD=pw") || !slices.Contains(req.Env, "PGDATABASE=forge") ||
					!slices.Contains(req.Env, "PGHOST=127.0.0.1") || !slices.Contains(req.Env, "PGPORT=5432") {
					t.Fatalf("connection: args %v env %v", req.Args, req.Env)
				}
				_, err := containerresulttest.WriteString(req.Stdout, "CREATE TABLE demo (id bigint);\n")
				if err != nil {
					return nil, err
				}
				if fails {
					return []byte("connection lost"), errors.New("exit 7")
				}
				return nil, nil
			})}
			err := builtinDump(t, scope, data).Dump(t.Context(), name)
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

// A dump whose end did not arrive (pg_dump exited 0, and the daemon lost
// the tail of what it printed) is no backup: nothing is published.
//
// Kills: a dump taken without its trailer checked.
func TestADumpWhoseEndDidNotArriveIsNoBackup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	name := filepath.Join(dir, "apptown-today.sql.gz")
	whole := containerresulttest.Framed([]byte("CREATE TABLE demo (id bigint);\nCOPY demo FROM stdin;\n1\n\\.\n"))
	scope := docker.Scope{Client: dumpCommand(func(_ context.Context, _ string, req docker.Command) ([]byte, error) {
		_, err := req.Stdout.Write(whole[:len(whole)-60])
		return nil, err
	})}
	err := builtinDump(t, scope, builtinDataDir(t)).Dump(t.Context(), name)
	files, _ := os.ReadDir(dir)
	if !errors.Is(err, containerresult.ErrCutShort) || len(files) != 0 {
		t.Fatalf("a dump cut short: %v, files %v; want it refused and nothing published", err, files)
	}
}

func TestDatabaseDumpCancellationLeavesNoBackup(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	scope := docker.Scope{Client: dumpCommand(func(ctx context.Context, _ string, req docker.Command) ([]byte, error) {
		if _, err := containerresulttest.WriteString(req.Stdout, "partial dump"); err != nil {
			return nil, err
		}
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})}
	dir := t.TempDir()
	dumpOf := builtinDump(t, scope, builtinDataDir(t))
	done := make(chan error, 1)
	go func() {
		done <- dumpOf.Dump(ctx, filepath.Join(dir, "dump.sql.gz"))
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
		if _, err := containerresulttest.WriteString(req.Stdout, "CREATE TABLE demo (id bigint);\n"); err != nil {
			return nil, err
		}
		if calls == 1 {
			return nil, failed
		}
		return nil, nil
	})}
	data := builtinDataDir(t)
	backup := databaseBackup{db: testDB(t), dump: builtinDump(t, scope, data), dataDir: data, enabled: true}
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
		if !slices.Contains(req.Args, "container:"+currentBuiltinDB.Container) || !slices.Contains(req.Env, "PGDATABASE=apptown") {
			t.Errorf("dumped %v %v, not the container and database db.env names", req.Args, req.Env)
		}
		_, err := containerresulttest.WriteString(req.Stdout, "CREATE TABLE demo (id bigint);\n")
		return nil, err
	})}
	backup := databaseBackup{db: testDB(t), dump: builtinDump(t, scope, dir), dataDir: dir, enabled: true, now: func() time.Time { return today }}
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
	return backupWorldVersion(t, during, "pg_dump (PostgreSQL) 16.12", nil)
}

func backupWorldVersion(t *testing.T, during func(), clientVersion string, versionErr error) *App {
	t.Helper()
	dir := t.TempDir()
	backupDataDirFixture(t, dir)
	f := newFakeDocker()
	results := &containerresulttest.Daemon{Answers: func(c containerresulttest.Command) (containerresulttest.Answer, bool) {
		if c.Image != dumpImage(t) || c.Script != "pg_dump --version" {
			return containerresulttest.Answer{}, false
		}
		if versionErr != nil {
			return containerresulttest.Answer{Code: 1, Said: versionErr.Error()}, true
		}
		// A first pull's messages reach the container's log, never the
		// version recorded.
		return containerresulttest.Answer{Answer: []byte(clientVersion + "\n"), Said: "Unable to find image locally\nPulling from library/postgres"}, true
	}}
	var command dockerClientFunc
	command = func(ctx context.Context, host string, req docker.Command) ([]byte, error) {
		if out, err, ok := results.Run(req.Args); ok {
			return out, err
		}

		if len(req.Args) > 2 && req.Args[0] == "run" && slices.Contains(req.Args, "container:apptown-db") && strings.HasPrefix(containerresulttest.UnframeLine(req.Args[len(req.Args)-1]), "'pg_dump'") {
			if during != nil {
				during()
			}
			_, err := containerresulttest.WriteString(req.Stdout, "-- PostgreSQL database dump\nCOPY public.projects (id) FROM stdin;\n1\n\\.\n")
			return nil, err
		}
		f.command = nil
		defer func() { f.command = command }()
		return f.Run(ctx, host, req)
	}
	f.command = command
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
	if p := report.Manifest.Database; p == nil || p.ServerVersionNum == 0 || p.DumpClientVersion != "pg_dump (PostgreSQL) 16.12" {
		t.Fatalf("missing observed database provenance: %+v", p)
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

// A whole-AppTown backup is AppTown only: it takes no copy of a standing app
// on the built-in host, reports no problem about one, and its manifest lists
// no apps. Their data comes back from their own deployment backups.
//
// Kills: a backup that still tries a copy of each app (and reports one it
// cannot read as a problem).
func TestABackupLeavesAppsAlone(t *testing.T) {
	t.Parallel()
	a := backupWorld(t, nil)
	ctx := t.Context()
	a.SeedTargets(ctx)
	createReadTestProject(t, a, "p")
	builtin, err := a.Deployment.Targets.ByName(ctx, deployment.BuiltInTargetName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Deployments.EnsureStanding(ctx, idstest.N(1), "main", builtin.ID, deployment.Attributes{}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	report, err := a.Backups.Backup(ctx, instancebackup.Request{Archive: filepath.Join(t.TempDir(), "apptown.backup"), Passphrase: backupPassphrase})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(report.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Problems) != 0 || strings.Contains(string(raw), `"deployments"`) {
		t.Fatalf("problems %v, manifest %s", report.Problems, raw)
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
// that cannot be rebuilt has its newest set named for a person.
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
		Trigger: dataprotection.TriggerPreRelease, Path: old + "/backups/p/main/2026-10-23", Status: dataprotection.BackupOK, Store: dataprotection.StoreAppTownDisk}, "test")
	if err != nil {
		t.Fatal(err)
	}
	m := &instancebackup.Manifest{AppTown: idstest.N(9), DataDir: old}
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
	if d, _ := a.Deployment.Deployments.ByID(ctx, local.ID); d.RestoreCheck != "" || len(report.NotFilled) != 1 || !strings.Contains(report.NotFilled[0], "release it again") || !strings.Contains(report.NotFilled[0], set.String()) {
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

// Failed observations cannot publish a new archive or leave work held.
func TestBackupRequiresObservedDumpClient(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, version string
		err           error
	}{
		{"unreadable", "", errors.New("client unavailable")},
		{"empty", "", nil},
		{"malformed", "not a PostgreSQL version", nil},
		// The test server is PostgreSQL 16: a 15 client cannot dump it, and
		// a 17 client's dump does not load into a 16 database.
		{"older than the server", "pg_dump (PostgreSQL) 15.8", nil},
		{"newer than the server", "pg_dump (PostgreSQL) 17.5", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dumped := false
			a := backupWorldVersion(t, func() { dumped = true }, tc.version, tc.err)
			archive := filepath.Join(t.TempDir(), "refused.backup")
			if _, err := a.Backups.Backup(t.Context(), instancebackup.Request{Archive: archive, Passphrase: backupPassphrase}); err == nil {
				t.Fatal("backup published without provenance")
			}
			if dumped {
				t.Fatal("the database was dumped before the client was checked")
			}
			if _, err := os.Stat(archive); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("archive exists: %v", err)
			}
			if err := a.newWork.Wait(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A -db-url database is dumped the one way the built-in one is: a
// throwaway container of the pinned postgres image for the server's major,
// on the daemon's default network, with the same dump (no owners, no
// privileges, plain, gzipped) so it loads into the built-in database a
// restore makes. The connection travels in libpq's environment, by name in
// docker's argv, never its value. A URL setting the backup does not pass on,
// a certificate file the container cannot read and a loopback host are
// each refused before docker is asked anything; a failed pg_dump leaves no
// file.
//
// Kills: the host's pg_dump; a dump that keeps the managed server's owners
// or grants; the password in argv; an unknown URL setting silently ignored;
// a partial dump published.
func TestAnExternalDatabaseIsDumpedInThePinnedPostgresImage(t *testing.T) {
	t.Parallel()
	image := dumpImage(t)
	dir := t.TempDir()
	var calls int
	scope := docker.Scope{Client: dumpCommand(func(_ context.Context, _ string, req docker.Command) ([]byte, error) {
		calls++
		if !slices.Equal(req.Args[:4], []string{"run", "--rm", "--entrypoint", ""}) || slices.Contains(req.Args, "--network") || !slices.Contains(req.Args, image) ||
			req.Args[len(req.Args)-1] != "'pg_dump' '--no-owner' '--no-privileges'" || strings.Contains(strings.Join(req.Args, " "), "s3cret") {
			t.Fatalf("dump command: %v", req.Args)
		}
		if _, err := containerresulttest.WriteString(req.Stdout, "SELECT 1;\n"); err != nil {
			return nil, err
		}
		if slices.Contains(req.Env, "PGDATABASE=broken") {
			return []byte("pg_dump: error: connection failed"), errors.New("exit 1")
		}
		for _, want := range []string{"PGHOST=db.internal", "PGPORT=25060", "PGUSER=doadmin", "PGPASSWORD=s3cret", "PGDATABASE=forge", "PGSSLMODE=require"} {
			if !slices.Contains(req.Env, want) {
				t.Fatalf("pg_dump's environment lacks %s: %v", want, req.Env)
			}
		}
		return nil, nil
	})}
	external := func(url string) databaseDump {
		return databaseDump{DB: testDB(t), Docker: scope, Tools: dumpTools(t), URL: url}
	}
	out := filepath.Join(dir, "database.sql.gz")
	if err := external("postgres://doadmin:s3cret@db.internal:25060/forge?sslmode=require").Dump(t.Context(), out); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(zr); string(body) != "SELECT 1;\n" {
		t.Fatalf("the dump holds %q", body)
	}

	failed := filepath.Join(dir, "failed.sql.gz")
	if err := external("postgres://u:p@h/broken").Dump(t.Context(), failed); err == nil || !strings.Contains(err.Error(), "connection failed") {
		t.Fatalf("a failed pg_dump: %v", err)
	}
	if _, err := os.Stat(failed); !os.IsNotExist(err) {
		t.Fatalf("a failed pg_dump left %s: %v", failed, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".apptown-dump-*")); len(left) != 0 {
		t.Fatalf("a failed pg_dump left %v", left)
	}
	before := calls
	for url, why := range map[string]string{
		"postgres://u:p@h/forge?application_name=x":                      "application_name",
		"postgres://u:p@h/forge?sslmode=verify-full&sslrootcert=/ca.pem": "sslrootcert",
		"postgres://u:p@localhost:5432/forge":                            "loopback",
		"postgres://u:p@127.0.0.1:5432/forge":                            "loopback",
		"postgres://u:p@[::1]:5432/forge":                                "loopback",
	} {
		if err := external(url).Dump(t.Context(), failed); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%s: %v, want a refusal naming %s", url, err, why)
		}
	}
	if calls != before {
		t.Fatalf("a refused URL reached docker")
	}
}

// A server of a major version the tool versions pin no postgres line for
// is refused before docker is asked anything: a dump is only ever taken by
// the server's own major, which a restore into the built-in database loads.
//
// Kills: a dump by another major's client.
func TestADumpOfAnUnpinnedMajorIsRefused(t *testing.T) {
	t.Parallel()
	raw := strings.Replace(string(stock.ToolVersions), "\n    \"16\": {version: \"16.", "\n    \"15\": {version: \"15.", 1)
	snap, err := toolversions.NewSnapshot("no-16", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap.RunImage("postgres", "16"); ok {
		t.Fatal("the fixture still pins postgres 16")
	}
	scope := docker.Scope{Client: dumpCommand(func(context.Context, string, docker.Command) ([]byte, error) {
		t.Fatal("docker asked for an unpinned major")
		return nil, nil
	})}
	d := databaseDump{DB: testDB(t), Docker: scope, Tools: toolversions.New(snap), DataDir: builtinDataDir(t)}
	if err := d.Dump(t.Context(), filepath.Join(t.TempDir(), "dump.sql.gz")); err == nil || !strings.Contains(err.Error(), "PostgreSQL 16 has no pinned pg_dump") {
		t.Fatalf("an unpinned major: %v", err)
	}
	if _, err := d.DumpClientVersion(t.Context()); err == nil || !strings.Contains(err.Error(), "PostgreSQL 16 has no pinned pg_dump") {
		t.Fatalf("the client of an unpinned major: %v", err)
	}
}
