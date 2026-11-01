package apptown

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/instancebackup"
	"github.com/flokkit/apptown/internal/backuparchive"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/toolversions"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/stock"
)

// dumpTasks is a backend dump tasks run on, for tests: what a task leaves
// and how it exits come from do, given the task's request; nothing runs.
// On AppTown's own Docker (worker false) the result is written into the
// folder the task mounts at /apptown/state; on a worker it is answered by
// Results.
type dumpTasks struct {
	t      *testing.T
	worker bool
	do     func(spec execution.TaskSpec) (result []byte, exit int, said string)
	// block makes Wait wait for its caller's end.
	block bool

	mu       sync.Mutex
	ensured  []execution.TaskSpec
	live     map[string]execution.TaskSpec
	exits    map[string]int
	said     map[string]string
	results  map[string][]byte
	resultOf map[string]string
	started  chan struct{}
}

// dumpsOn is a place dumps run on: a worker, or this AppTown's own Docker.
func dumpsOn(t *testing.T, worker bool, do func(execution.TaskSpec) ([]byte, int, string)) (*dumpPlace, *dumpTasks) {
	f := &dumpTasks{t: t, worker: worker, do: do, live: map[string]execution.TaskSpec{}, exits: map[string]int{}, said: map[string]string{}, results: map[string][]byte{}, resultOf: map[string]string{}, started: make(chan struct{}, 8)}
	p := &dumpPlace{Tasks: f, Scratch: filepath.Join(t.TempDir(), "dump-tasks"), Profile: "local-docker"}
	if worker {
		p.Results, p.Scratch, p.Profile = f, "", "worker"
	}
	return p, f
}

// scriptOf is the shell line a dump task runs.
func scriptOf(spec execution.TaskSpec) string { return spec.Argv[len(spec.Argv)-1] }

// fileIn is the content of the generated file at path in spec, and its mode.
func fileIn(spec execution.TaskSpec, path string) (string, os.FileMode, bool) {
	for _, f := range spec.Files {
		if f.Path == path {
			return string(f.Content), f.Mode, true
		}
	}
	return "", 0, false
}

func (f *dumpTasks) Capabilities() execution.Capabilities {
	return execution.Capabilities{Results: f.worker}
}

func (f *dumpTasks) Ensure(_ context.Context, spec execution.TaskSpec) (execution.TaskObservation, error) {
	if err := spec.Validate(time.Now()); err != nil {
		return execution.TaskObservation{}, err
	}
	if err := f.Capabilities().Unclaimed(spec); err != nil {
		return execution.TaskObservation{}, err
	}
	if f.worker && (len(spec.Mounts) > 0 || spec.Network != "") {
		return execution.TaskObservation{}, fmt.Errorf("%w: a worker shares no files or host routes with AppTown", execution.ErrUnsupported)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, spec)
	f.live[spec.Key] = spec
	return execution.TaskObservation{ID: "id-" + spec.Key, State: execution.TaskCreated, ExitCode: -1}, nil
}

func (f *dumpTasks) Start(_ context.Context, ref execution.TaskRef) error {
	f.mu.Lock()
	spec, ok := f.live[ref.Key]
	f.mu.Unlock()
	if !ok {
		return fmt.Errorf("no task %s", ref.Key)
	}
	result, exit, said := f.do(spec)
	script := scriptOf(spec)
	name := script[strings.LastIndex(script, "/")+1:]
	f.mu.Lock()
	f.exits[ref.Key], f.said[ref.Key], f.resultOf[ref.Key] = exit, said, name
	if result != nil {
		if f.worker {
			f.results[ref.Key] = result
		} else {
			host, _, _ := strings.Cut(spec.Mounts[0], ":")
			if err := os.WriteFile(filepath.Join(host, name), result, 0o644); err != nil {
				f.t.Errorf("writing the task's result: %v", err)
			}
		}
	}
	f.mu.Unlock()
	f.started <- struct{}{}
	return nil
}

func (f *dumpTasks) Observe(context.Context, execution.TaskRef) (execution.TaskObservation, error) {
	return execution.TaskObservation{}, errors.New("not used")
}

func (f *dumpTasks) Wait(ctx context.Context, ref execution.TaskRef) (int, error) {
	if f.block {
		<-ctx.Done()
		return -1, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exits[ref.Key], nil
}

func (f *dumpTasks) Output(_ context.Context, ref execution.TaskRef, _ execution.OutputRead) (execution.OutputPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return execution.OutputPage{Data: []byte(f.said[ref.Key])}, nil
}

func (f *dumpTasks) Stop(context.Context, execution.TaskRef) error { return nil }

func (f *dumpTasks) Release(_ context.Context, ref execution.TaskRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.live, ref.Key)
	return nil
}

func (f *dumpTasks) ReleaseWorkspace(context.Context, string) error { return execution.ErrUnsupported }

func (f *dumpTasks) Results(_ context.Context, ref execution.TaskRef) (map[string]execution.ResultFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, ok := f.results[ref.Key]
	if !ok {
		return map[string]execution.ResultFile{f.resultOf[ref.Key]: {State: execution.ResultAbsent}}, nil
	}
	if int64(len(body)) > execution.MaxResultBytes {
		return map[string]execution.ResultFile{f.resultOf[ref.Key]: {State: execution.ResultTooLarge, Size: int64(len(body))}}, nil
	}
	return map[string]execution.ResultFile{f.resultOf[ref.Key]: {State: execution.ResultPresent, Content: body, Size: int64(len(body))}}, nil
}

// released says every task ensured was released.
func (f *dumpTasks) released() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.live) == 0
}

// gz is body gzipped, as a dump task leaves it.
func gz(t *testing.T, body string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// gunzipFile is the content of the gzipped file at path.
func gunzipFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// dumpTools is the stock tool versions, whose postgres line for the newest
// supported major pins the image a dump runs in; the test database is
// PostgreSQL 16.
func dumpTools(t *testing.T) *toolversions.Versions {
	t.Helper()
	snap, err := toolversions.NewSnapshot("stock", stock.ToolVersions)
	if err != nil {
		t.Fatal(err)
	}
	return toolversions.New(snap)
}

// dumpImage is the image every dump runs in.
func dumpImage(t *testing.T) string {
	t.Helper()
	image, ok := dumpTools(t).Current().RunImage("postgres", strconv.Itoa(database.OwnPostgresMax))
	if !ok || !strings.Contains(image, "@sha256:") {
		t.Fatalf("no pinned postgres %d run image: %q", database.OwnPostgresMax, image)
	}
	return image
}

// builtinDump is the dump of the built-in database db.env under dataDir
// names, on place.
func builtinDump(t *testing.T, place *dumpPlace, dataDir string) databaseDump {
	return databaseDump{DB: testDB(t), Place: place, Tools: dumpTools(t), DataDir: dataDir}
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

// The built-in database is dumped by a task of the pinned postgres image on
// AppTown's own Docker, joining the database container's network, reading
// the database db.env's URL names (an install made before 1.0 keeps the
// database forge, one made since has apptown), under pipefail after the
// trusted authorities are installed. The password is in a generated
// password file, never in argv or the environment. The dump is published
// only when the task exited 0 and left it; the task is released either way.
//
// Kills: the password in argv or the environment; output published from a
// failed task; a task left behind.
func TestDatabaseDumpPublishesOnlyCompleteOutput(t *testing.T) {
	t.Parallel()
	image := dumpImage(t)
	before1 := builtinDBNames{Container: "forge-db", Volume: "forge-data-forge-db", Database: "forge"}
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "producer fails after output"}[fails], func(t *testing.T) {
			dir := t.TempDir()
			data := dataDirWithDB(t, before1.Container, before1.Database)
			name := filepath.Join(dir, "today's $HOME backup.sql.gz")
			place, tasks := dumpsOn(t, false, func(spec execution.TaskSpec) ([]byte, int, string) {
				script := scriptOf(spec)
				if spec.Image != image || spec.Network != "container:forge-db" || spec.Argv[0] != "bash" || !slices.Contains(spec.Argv, "pipefail") ||
					!strings.HasPrefix(script, trust.Run+"; ") || !strings.Contains(script, "pg_dump --no-owner --no-privileges | gzip") {
					t.Fatalf("dump task: %+v", spec)
				}
				if strings.Contains(strings.Join(spec.Argv, " ")+strings.Join(spec.Env, " "), "pw") || !slices.Contains(spec.Env, "PGDATABASE=forge") ||
					!slices.Contains(spec.Env, "PGHOST=127.0.0.1") || !slices.Contains(spec.Env, "PGPORT=5432") || !slices.Contains(spec.Env, "PGPASSFILE="+dumpPassFile) {
					t.Fatalf("connection: argv %v env %v", spec.Argv, spec.Env)
				}
				if pass, mode, ok := fileIn(spec, dumpPassFile); !ok || pass != "*:*:*:*:pw\n" || mode != 0o600 {
					t.Fatalf("password file: %q %v %v", pass, mode, ok)
				}
				if fails {
					return gz(t, "CREATE TABLE demo"), 7, "connection lost"
				}
				return gz(t, "CREATE TABLE demo (id bigint);\n"), 0, ""
			})
			err := builtinDump(t, place, data).Dump(t.Context(), name)
			files, readErr := os.ReadDir(dir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !tasks.released() {
				t.Fatal("the dump's task was not released")
			}
			if fails {
				if err == nil || !strings.Contains(err.Error(), "connection lost") || len(files) != 0 {
					t.Fatalf("failed dump published or left scratch: %v %v", err, files)
				}
				return
			}
			if err != nil || len(files) != 1 {
				t.Fatalf("successful dump: %v %v", err, files)
			}
			if got := gunzipFile(t, name); got != "CREATE TABLE demo (id bigint);\n" {
				t.Fatalf("dump bytes: %q", got)
			}
			if left, _ := os.ReadDir(place.Scratch); len(left) != 0 {
				t.Fatalf("the task's state folder was left: %v", left)
			}
		})
	}
}

// A task that exited 0 and left no dump is no backup: nothing is published.
//
// Kills: a dump published without the task's file.
func TestADumpTaskThatLeftNoDumpIsNoBackup(t *testing.T) {
	t.Parallel()
	for _, worker := range []bool{false, true} {
		dir := t.TempDir()
		place, _ := dumpsOn(t, worker, func(execution.TaskSpec) ([]byte, int, string) { return nil, 0, "" })
		d := databaseDump{DB: testDB(t), Place: place, Tools: dumpTools(t), URL: "postgres://u:p@db.internal/forge"}
		err := d.Dump(t.Context(), filepath.Join(dir, "dump.sql.gz"))
		files, _ := os.ReadDir(dir)
		if err == nil || len(files) != 0 {
			t.Fatalf("worker %v: a task that left no dump: %v, files %v; want it refused and nothing published", worker, err, files)
		}
	}
}

func TestDatabaseDumpCancellationLeavesNoBackup(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	place, tasks := dumpsOn(t, false, func(execution.TaskSpec) ([]byte, int, string) { return gz(t, "partial dump"), 0, "" })
	tasks.block = true
	dir := t.TempDir()
	dumpOf := builtinDump(t, place, builtinDataDir(t))
	done := make(chan error, 1)
	go func() {
		done <- dumpOf.Dump(ctx, filepath.Join(dir, "dump.sql.gz"))
	}()
	<-tasks.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dump did not stop after cancellation")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 || !tasks.released() {
		t.Fatalf("cancelled dump left files %v (%v) or its task (released %v)", files, err, tasks.released())
	}
}

func TestDatabaseBackupJobFailureDoesNotClaimTodaysDump(t *testing.T) {
	t.Parallel()
	calls := 0
	place, _ := dumpsOn(t, false, func(execution.TaskSpec) ([]byte, int, string) {
		calls++
		if calls == 1 {
			return gz(t, "CREATE TABLE demo"), 1, "dump interrupted"
		}
		return gz(t, "CREATE TABLE demo (id bigint);\n"), 0, ""
	})
	data := builtinDataDir(t)
	backup := databaseBackup{db: testDB(t), dump: builtinDump(t, place, data), dataDir: data, enabled: true}
	if receipt, err := backup.Run(t.Context(), "daily", nil); err == nil || !strings.Contains(err.Error(), "dump interrupted") || receipt != nil {
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
	place, _ := dumpsOn(t, false, func(spec execution.TaskSpec) ([]byte, int, string) {
		if spec.Network != "container:"+currentBuiltinDB.Container || !slices.Contains(spec.Env, "PGDATABASE=apptown") {
			t.Errorf("dumped %v %v, not the container and database db.env names", spec.Network, spec.Env)
		}
		return gz(t, "CREATE TABLE demo (id bigint);\n"), 0, ""
	})
	backup := databaseBackup{db: testDB(t), dump: builtinDump(t, place, dir), dataDir: dir, enabled: true, now: func() time.Time { return today }}
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
	a := testApp(t, Config{DataDir: dir, DockerClient: newFakeDocker(), UsingBuiltinDB: true})
	place, _ := dumpsOn(t, false, func(spec execution.TaskSpec) ([]byte, int, string) {
		if spec.Image != dumpImage(t) {
			t.Errorf("a dump task of %s", spec.Image)
		}
		if strings.Contains(scriptOf(spec), "pg_dump --version") {
			if versionErr != nil {
				return nil, 1, versionErr.Error()
			}
			// A first pull's messages reach the task's output, never the
			// version recorded.
			return []byte(clientVersion + "\n"), 0, "Unable to find image locally\nPulling from library/postgres"
		}
		if spec.Network != "container:apptown-db" {
			t.Errorf("the dump task's network: %q", spec.Network)
		}
		if during != nil {
			during()
		}
		return gz(t, "-- PostgreSQL database dump\nCOPY public.projects (id) FROM stdin;\n1\n\\.\n"), 0, ""
	})
	a.dumps = *place
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
		mover, err := exclusiveBackupFiles(filepath.Join(a.cfg.dataDir, "backups"))
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
	if l, err := exclusiveBackupFiles(filepath.Join(a.cfg.dataDir, "backups")); err != nil {
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
				mover, err := exclusiveBackupFiles(filepath.Join(a.cfg.dataDir, "backups"))
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

// A -db-url database is dumped the one way the built-in one is: a task of
// the pinned postgres image, on the backend's default network, with the
// same dump (no owners, no privileges, plain, gzipped) so it loads into the
// built-in database a restore makes. The password travels in a generated
// password file, escaped as libpq reads it, never in argv or the
// environment; sslmode is passed as written. A root certificate the URL
// names is given to the task as a file; verify-ca and verify-full without
// one verify against the image's system roots, which trust.Run extends. A
// URL setting the backup does not pass on and a loopback host are refused
// before a task is made; a failed pg_dump leaves no file.
//
// Kills: a dump that keeps the managed server's owners or grants; the
// password in argv or the environment; sslrootcert refused; verify-ca
// pointed at libpq's "system" (which refuses it); an unknown URL setting
// silently ignored; a partial dump published.
func TestAnExternalDatabaseIsDumpedInThePinnedPostgresImage(t *testing.T) {
	t.Parallel()
	image := dumpImage(t)
	dir := t.TempDir()
	rootCert := filepath.Join(dir, "ca-certificate.crt")
	if err := os.WriteFile(rootCert, []byte("-----BEGIN CERTIFICATE-----\nDO\n-----END CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var last execution.TaskSpec
	place, tasks := dumpsOn(t, false, func(spec execution.TaskSpec) ([]byte, int, string) {
		last = spec
		if spec.Image != image || spec.Network != "" || !strings.Contains(scriptOf(spec), "pg_dump --no-owner --no-privileges | gzip") ||
			strings.Contains(strings.Join(spec.Argv, " ")+strings.Join(spec.Env, " "), "s3c") {
			t.Fatalf("dump task: %+v", spec)
		}
		if slices.Contains(spec.Env, "PGDATABASE=broken") {
			return nil, 1, "pg_dump: error: connection failed"
		}
		return gz(t, "SELECT 1;\n"), 0, ""
	})
	external := func(url string) databaseDump {
		return databaseDump{DB: testDB(t), Place: place, Tools: dumpTools(t), URL: url}
	}
	out := filepath.Join(dir, "database.sql.gz")
	if err := external(`postgres://doadmin:s3c%3Ar%5Cet@db.internal:25060/forge?sslmode=require`).Dump(t.Context(), out); err != nil {
		t.Fatal(err)
	}
	if body := gunzipFile(t, out); body != "SELECT 1;\n" {
		t.Fatalf("the dump holds %q", body)
	}
	for _, want := range []string{"PGHOST=db.internal", "PGPORT=25060", "PGUSER=doadmin", "PGDATABASE=forge", "PGSSLMODE=require", "PGPASSFILE=" + dumpPassFile} {
		if !slices.Contains(last.Env, want) {
			t.Fatalf("pg_dump's environment lacks %s: %v", want, last.Env)
		}
	}
	if pass, mode, _ := fileIn(last, dumpPassFile); pass != `*:*:*:*:s3c\:r\\et`+"\n" || mode != 0o600 {
		t.Fatalf("the password file: %q %v", pass, mode)
	}
	if slices.ContainsFunc(last.Env, func(e string) bool { return strings.HasPrefix(e, "PGSSLROOTCERT=") }) {
		t.Fatalf("sslmode=require given a root certificate: %v", last.Env)
	}

	for url, root := range map[string]string{
		"postgres://u:p@db.internal/forge?sslmode=verify-ca":                           dumpSystemRoots,
		"postgres://u:p@db.internal/forge?sslmode=verify-full":                         dumpSystemRoots,
		"postgres://u:p@db.internal/forge?sslmode=verify-full&sslrootcert=" + rootCert: dumpRootCert,
	} {
		if err := external(url).Dump(t.Context(), out); err != nil {
			t.Fatalf("%s: %v", url, err)
		}
		mode := url[strings.Index(url, "sslmode=")+len("sslmode="):]
		mode, _, _ = strings.Cut(mode, "&")
		if !slices.Contains(last.Env, "PGSSLROOTCERT="+root) || !slices.Contains(last.Env, "PGSSLMODE="+mode) {
			t.Fatalf("%s: environment %v", url, last.Env)
		}
		if cert, _, ok := fileIn(last, dumpRootCert); ok != (root == dumpRootCert) || (ok && !strings.Contains(cert, "DO")) {
			t.Fatalf("%s: the root certificate file %q %v", url, cert, ok)
		}
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
	before := len(tasks.ensured)
	for url, why := range map[string]string{
		"postgres://u:p@h/forge?application_name=x":                              "application_name",
		"postgres://u:p@h/forge?sslmode=verify-full&sslrootcert=/no/such/ca.pem": "sslrootcert",
		"postgres://u:p@localhost:5432/forge":                                    "loopback",
		"postgres://u:p@127.0.0.1:5432/forge":                                    "loopback",
		"postgres://u:p@[::1]:5432/forge":                                        "loopback",
	} {
		if err := external(url).Dump(t.Context(), failed); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%s: %v, want a refusal naming %s", url, err, why)
		}
	}
	if len(tasks.ensured) != before {
		t.Fatalf("a refused URL made a task")
	}
}

// With no Docker of its own, AppTown dumps a -db-url database on its
// worker: the task mounts nothing, declares its dump as a result, and the
// worker's answer is the dump; the version is read the same way. A dump
// over what a worker task may leave is refused, never truncated; the
// built-in database, only on AppTown's own Docker, is not dumped there.
//
// Kills: a whole-AppTown backup refused for want of local Docker; a dump
// on a worker published from a result that is not present.
func TestWithNoLocalDockerTheDumpRunsOnTheWorker(t *testing.T) {
	t.Parallel()
	big := false
	place, tasks := dumpsOn(t, true, func(spec execution.TaskSpec) ([]byte, int, string) {
		if len(spec.Mounts) > 0 || len(spec.Results) != 1 || !strings.HasPrefix(spec.Results[0].Path, execution.StateRoot+"/") || spec.Admission.Profile != "worker" {
			t.Fatalf("a worker's dump task: %+v", spec)
		}
		if strings.Contains(scriptOf(spec), "--version") {
			return []byte("pg_dump (PostgreSQL) 16.15\n"), 0, ""
		}
		if big {
			return make([]byte, execution.MaxResultBytes+1), 0, ""
		}
		return gz(t, "SELECT 1;\n"), 0, ""
	})
	d := databaseDump{DB: testDB(t), Place: place, Tools: dumpTools(t), URL: "postgres://doadmin:pw@private-db.example:25060/forge?sslmode=require"}
	out := filepath.Join(t.TempDir(), "database.sql.gz")
	if err := d.Dump(t.Context(), out); err != nil {
		t.Fatal(err)
	}
	if body := gunzipFile(t, out); body != "SELECT 1;\n" {
		t.Fatalf("the dump holds %q", body)
	}
	if v, err := d.DumpClientVersion(t.Context()); err != nil || v != "pg_dump (PostgreSQL) 16.15" {
		t.Fatalf("the client version: %q %v", v, err)
	}
	big = true
	tooBig := filepath.Join(t.TempDir(), "big.sql.gz")
	if err := d.Dump(t.Context(), tooBig); err == nil || !strings.Contains(err.Error(), "over the") {
		t.Fatalf("a dump over a worker's limit: %v", err)
	}
	if _, err := os.Stat(tooBig); !os.IsNotExist(err) {
		t.Fatalf("a dump over a worker's limit was published: %v", err)
	}
	builtin := databaseDump{DB: testDB(t), Place: place, Tools: dumpTools(t), DataDir: builtinDataDir(t)}
	if err := builtin.Dump(t.Context(), filepath.Join(t.TempDir(), "builtin.sql.gz")); err == nil {
		t.Fatal("the built-in database was dumped on a worker")
	}
	if !tasks.released() {
		t.Fatal("a worker's dump task was not released")
	}
}

// A dump runs in the pinned postgres image of the newest major AppTown
// supports for its own database; without one, it is refused before a task
// is made. The built-in database is made at that major.
//
// Kills: a dump by another major's client; a built-in database made at a
// major the dump does not cover.
func TestADumpWithoutThePinnedNewestMajorIsRefused(t *testing.T) {
	t.Parallel()
	if !strings.HasPrefix(builtinDBImage, "postgres:"+strconv.Itoa(database.OwnPostgresMax)+"-") {
		t.Fatalf("the built-in database is made with %s, not PostgreSQL %d", builtinDBImage, database.OwnPostgresMax)
	}
	raw := strings.Replace(string(stock.ToolVersions), "\n    \"16\": {version: \"16.", "\n    \"15\": {version: \"15.", 1)
	snap, err := toolversions.NewSnapshot("no-16", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap.RunImage("postgres", strconv.Itoa(database.OwnPostgresMax)); ok {
		t.Fatal("the fixture still pins the newest major")
	}
	place, tasks := dumpsOn(t, false, func(execution.TaskSpec) ([]byte, int, string) {
		t.Fatal("a task made with no pinned image")
		return nil, 1, ""
	})
	d := databaseDump{DB: testDB(t), Place: place, Tools: toolversions.New(snap), DataDir: builtinDataDir(t)}
	want := "pin no PostgreSQL " + strconv.Itoa(database.OwnPostgresMax)
	if err := d.Dump(t.Context(), filepath.Join(t.TempDir(), "dump.sql.gz")); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("no pinned image: %v", err)
	}
	if _, err := d.DumpClientVersion(t.Context()); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("the client with no pinned image: %v", err)
	}
	if len(tasks.ensured) != 0 {
		t.Fatal("a task was made")
	}
}

// ensureInterrupted is a backend whose Ensure made the task and then lost
// its answer (the caller cancelled, or the reply did not arrive).
type ensureInterrupted struct{ *dumpTasks }

func (d ensureInterrupted) Ensure(ctx context.Context, spec execution.TaskSpec) (execution.TaskObservation, error) {
	if _, err := d.dumpTasks.Ensure(ctx, spec); err != nil {
		return execution.TaskObservation{}, err
	}
	return execution.TaskObservation{}, context.Canceled
}

// A dump task is released by its key even when its Ensure failed: an
// Ensure that fails may still have made the task or staged its password
// file.
//
// Kills: cleanup registered only after a successful Ensure.
func TestADumpTaskWhoseEnsureFailedIsReleased(t *testing.T) {
	t.Parallel()
	place, tasks := dumpsOn(t, false, nil)
	place.Tasks = ensureInterrupted{tasks}
	if err := place.run(t.Context(), "fake", dumpConn{}, "true", "dump.sql.gz", io.Discard); err == nil {
		t.Fatal("an interrupted Ensure reported success")
	}
	if !tasks.released() {
		t.Fatal("an interrupted Ensure left its task")
	}
}

// An independent process holding the retention lock must exclude an archive.
func exclusiveBackupFiles(directory string) (io.Closer, error) {
	f, err := os.OpenFile(filepath.Clean(directory)+".retention.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}
