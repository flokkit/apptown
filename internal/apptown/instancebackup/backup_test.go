package instancebackup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/backuparchive"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

const backupPassphrase = "a passphrase for tests"

type dockerClientFunc func(context.Context, string, docker.Command) ([]byte, error)

func (f dockerClientFunc) Run(ctx context.Context, host string, req docker.Command) ([]byte, error) {
	return f(ctx, host, req)
}

// dataDirFixture is a data directory with AppTown's key files, an SSH key,
// a project's folder, and what an archive leaves out.
func dataDirFixture(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"runtime-token-key":             strings.Repeat("k", 32),
		"secrets.env":                   "INFISICAL_URL=https://vault.example\nINFISICAL_CLIENT_ID=c\nINFISICAL_CLIENT_SECRET=s\nINFISICAL_PROJECT_ID=p1\n",
		"db.env":                        "APPTOWN_DB_URL=postgres://postgres:pw@127.0.0.1:54321/apptown\nAPPTOWN_DB_PASSWORD=pw\nAPPTOWN_DB_CONTAINER=apptown-db\nAPPTOWN_DB_VOLUME=apptown-data-apptown-db\n",
		"instance-id":                   "abc123\n",
		"ssh/host-key":                  "ssh key",
		"workspaces/project-p/README":   "the project",
		"pgdata/PG_VERSION":             "16",
		"pgdata/.apptown-docker-volume": `{"version":1,"volume":"source-host-only"}`,
		"cache/mise/tool":               "a tool",
		"backups/.staging/take-x/file":  "staged",
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

// An archive holds the manifest, the dump and the data directory without
// pgdata, cache and staged copies; unpacked into an empty directory it
// gives back the files, and the key files and the dump are held to the
// manifest's digests. A key file that does not match, a database newer
// than this build, and another passphrase are refused by name.
func TestAnArchiveUnpacksWhatItHoldsAndHoldsItToItsManifest(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	dataDirFixture(t, src)
	dump := filepath.Join(t.TempDir(), "dump.sql.gz")
	if err := os.WriteFile(dump, []byte("the dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	write := func(edit func(*Manifest)) string {
		m := Manifest{DataDir: src, Format: ArchiveFormat, Version: ArchiveVersion, Schema: dbschema.Latest(), KeyFiles: map[string]backuparchive.Digest{}}
		m.Dump, _ = backuparchive.SumFile(dump)
		keys, err := keyFileNames(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range keys {
			m.KeyFiles[k], _ = backuparchive.SumFile(filepath.Join(src, k))
		}
		if edit != nil {
			edit(&m)
		}
		path := filepath.Join(t.TempDir(), "apptown.backup")
		if _, _, err := writeArchive(path, backupPassphrase, &m, dump, src, ".apptown-backup-x"); err != nil {
			t.Fatal(err)
		}
		return path
	}
	unpack := func(path, passphrase string) (*Manifest, string, error) {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		dst := t.TempDir()
		m, err := unpack(f, passphrase, dst, filepath.Join(t.TempDir(), "dump"), func(string) {})
		return m, dst, err
	}
	m, dst, err := unpack(write(nil), backupPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.KeyFiles) != 5 || m.KeyFiles["ssh/host-key"].Bytes != 7 {
		t.Fatalf("the manifest's key files: %v", m.KeyFiles)
	}
	for name, want := range map[string]bool{"workspaces/project-p/README": true, "secrets.env": true, "ssh/host-key": true,
		"pgdata/PG_VERSION": false, "pgdata/.apptown-docker-volume": false, "cache/mise/tool": false, "backups/.staging/take-x/file": false} {
		if _, err := os.Stat(filepath.Join(dst, name)); (err == nil) != want {
			t.Errorf("%s came back: %v, want %v", name, err == nil, want)
		}
	}
	if _, _, err := unpack(write(func(m *Manifest) {
		m.KeyFiles["runtime-token-key"] = backuparchive.Digest{SHA256: "00", Bytes: 3}
	}), backupPassphrase); err == nil || !strings.Contains(err.Error(), "runtime-token-key") {
		t.Fatalf("a key file unlike the manifest's: %v", err)
	}
	if _, _, err := unpack(write(func(m *Manifest) { m.Schema = dbschema.Latest() + 1 }), backupPassphrase); err == nil || !strings.Contains(err.Error(), "newer than this build") {
		t.Fatalf("a newer database: %v", err)
	}
	// RC7 archives predate database provenance; they remain valid archives.
	if m, _, err := unpack(write(func(m *Manifest) { m.Schema = 167; m.Database = nil }), backupPassphrase); err != nil || m.Schema != 167 || m.Database != nil {
		t.Fatalf("RC7 archive without provenance: %+v, %v", m, err)
	}
	for _, schema := range []int{0, 166} {
		archive := write(func(m *Manifest) { m.Schema = schema })
		if _, dst, err := unpack(archive, backupPassphrase); err == nil || !strings.Contains(err.Error(), "schema 167 or newer") {
			t.Fatalf("unsupported schema %d: %v", schema, err)
		} else if entries, err := os.ReadDir(dst); err != nil || len(entries) != 0 {
			t.Fatalf("archive extracted before refusal: %v, %v", entries, err)
		}
		dst := t.TempDir()
		dk := docker.Scope{Client: dockerClientFunc(func(_ context.Context, _ string, req docker.Command) ([]byte, error) {
			if len(req.Args) == 0 || req.Args[0] != "ps" {
				t.Fatalf("restore changed Docker before refusal: %v", req.Args)
			}
			return nil, nil
		})}
		if _, err := Restore(t.Context(), RestoreRequest{DataDir: dst, Archive: archive, Passphrase: backupPassphrase, Docker: dk, Database: forbiddenRestoreDatabase{t: t}}); err == nil || !strings.Contains(err.Error(), "schema 167 or newer") {
			t.Fatalf("Restore unsupported schema %d: %v", schema, err)
		}
		if entries, err := os.ReadDir(dst); err != nil || len(entries) != 0 {
			t.Fatalf("Restore extracted before refusal: %v, %v", entries, err)
		}
	}
	if _, _, err := unpack(write(nil), "another passphrase entirely"); !errors.Is(err, backuparchive.ErrPassphrase) {
		t.Fatalf("another passphrase: %v", err)
	}
}

// The dump is loaded into the database as it is, and the rows of each
// table's COPY are counted on the way: a database that ends up with other
// counts is not the dump.
func TestTheDumpsRowsAreCountedAsItLoads(t *testing.T) {
	t.Parallel()
	dump := "SET x = 1;\nCOPY public.projects (id, slug) FROM stdin;\n1\tp\n2\tq\n\\.\nCOPY public.empty (id) FROM stdin;\n\\.\nSELECT 1;\n"
	var out bytes.Buffer
	rows := map[string]int64{}
	if err := countCopyRows(strings.NewReader(dump), &out, rows); err != nil {
		t.Fatal(err)
	}
	if out.String() != dump {
		t.Fatal("the dump was changed on its way to the database")
	}
	if rows["public.projects"] != 2 || rows["public.empty"] != 0 || len(rows) != 2 {
		t.Fatalf("counted %v", rows)
	}
}

// A restore goes onto a host with no AppTown: a data directory with
// anything in it, the built-in database's container, and any container
// AppTown labelled are each refused by name.
func TestARestoreRefusesAHostWithAnAppTown(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"empty", "data", "database", "labelled"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "data")
			if scenario == "data" {
				dataDirFixture(t, dir)
			}
			dk := docker.Scope{Client: dockerClientFunc(func(_ context.Context, _ string, req docker.Command) ([]byte, error) {
				args := strings.Join(req.Args, " ")
				switch {
				case strings.HasPrefix(args, "ps") && scenario == "database" && strings.Contains(args, "label=apptown.instance"):
					return []byte("apptown-db\n"), nil
				case strings.HasPrefix(args, "ps") && scenario == "labelled" && strings.Contains(args, "label=apptown.deployment"):
					return []byte("apptown-dep-1234\n"), nil
				}
				return nil, nil
			})}
			err := refuseExistingAppTown(t.Context(), dk, dir)
			want := map[string]string{"data": "not empty", "database": "apptown-db", "labelled": "apptown-dep-1234"}[scenario]
			if (err == nil) != (want == "") || (want != "" && !strings.Contains(err.Error(), want)) {
				t.Fatalf("%v, want %q", err, want)
			}
		})
	}
}

// `apptownd backup` and the running AppTown speak one request and its
// steps over the control socket, each reply a line: a request AppTown does
// not know, and a backup it refuses, end in an error line; an AppTown that
// is not there is said to be unreachable.
func TestTheControlSocketAnswersOneRequest(t *testing.T) {
	t.Parallel()
	s := New(Dependencies{DataDir: t.TempDir(), NewWork: &Hold{}})
	ask := func(req ControlRequest) (ControlReply, error) {
		server, client := net.Pipe()
		go s.serveControl(t.Context(), server)
		defer client.Close()
		line, _ := json.Marshal(req)
		if _, err := client.Write(append(line, '\n')); err != nil {
			return ControlReply{}, err
		}
		var r ControlReply
		err := json.NewDecoder(client).Decode(&r)
		return r, err
	}
	if r, err := ask(ControlRequest{Op: "format the disk"}); err != nil || !strings.Contains(r.Error, "unknown request") {
		t.Fatalf("an unknown request: %+v %v", r, err)
	}
	if r, err := ask(ControlRequest{Op: "backup", Archive: filepath.Join(t.TempDir(), "a.backup"), Passphrase: backupPassphrase}); err != nil || !strings.Contains(r.Error, "not the built-in one") {
		t.Fatalf("a backup of an AppTown on another database: %+v %v", r, err)
	}
	if _, err := AskControl(t.Context(), t.TempDir(), ControlRequest{Op: "backup"}, nil); err == nil || !strings.Contains(err.Error(), "cannot reach") {
		t.Fatalf("no AppTown running: %v", err)
	}
}

// One hold at a time; waiting returns at once while nothing is held, once
// the hold is released, or with the context's end.
func TestAHoldHoldsUntilReleased(t *testing.T) {
	t.Parallel()
	var h Hold
	if err := h.Wait(t.Context()); err != nil {
		t.Fatal("nothing held, and Wait waited")
	}
	release, ok := h.Hold()
	if !ok {
		t.Fatal("the first hold was refused")
	}
	if _, again := h.Hold(); again {
		t.Fatal("a second hold was granted while one is held")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := h.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("a held wait whose context ended: %v", err)
	}
	release()
	release()
	if err := h.Wait(t.Context()); err != nil {
		t.Fatal("released, and Wait still waited")
	}
}

// No provider operation is permitted before the archive's schema is accepted.
type forbiddenRestoreDatabase struct{ t *testing.T }

func (d forbiddenRestoreDatabase) Names() (string, string, bool) {
	d.t.Fatal("Names before archive acceptance")
	return "", "", false
}
func (d forbiddenRestoreDatabase) Dump(context.Context, string) error {
	d.t.Fatal("Dump during restore")
	return nil
}
func (d forbiddenRestoreDatabase) DumpClientVersion(context.Context) (string, error) {
	d.t.Fatal("DumpClientVersion during restore")
	return "", nil
}
func (d forbiddenRestoreDatabase) Ensure(context.Context) (string, error) {
	d.t.Fatal("Ensure before archive acceptance")
	return "", nil
}
