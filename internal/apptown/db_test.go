package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/dockerlabel"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/strs"
)

var databaseSuite = dbtest.NewSuite(OpenURL)

func testDB(t *testing.T) *database.DB { t.Helper(); return databaseSuite.New(t) }
func TestMain(m *testing.M)            { os.Exit(databaseSuite.Run(m)) }

// Errors that mean something: no read may
// discard its outcome, no write may be log-and-continue under the old
// name. Sorted reads use lookup or readOK; sorted writes use exec,
// stateWrite or logWrite.
func TestNoSilentReadsOrWrites(t *testing.T) {
	t.Parallel()
	bareRead := regexp.MustCompile(`(?m)^\s*_ = [A-Za-z.]*\.QueryRow(?:Context)?\(`)
	ignoredScan := regexp.MustCompile(`(?m)^\s*_ = [A-Za-z]+\.Scan\(`)
	err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(raw)
		if m := bareRead.FindString(src); m != "" {
			t.Errorf("%s: read discards its outcome: %s", path, strings.TrimSpace(m))
		}
		if m := ignoredScan.FindString(src); m != "" {
			t.Errorf("%s: ignored Scan: %s", path, strings.TrimSpace(m))
		}
		if strings.Contains(src, "a.bgWrite(") {
			t.Errorf("%s: bgWrite is gone; choose checked writes or explicit failure logging", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// mustJSON encodes a fixture; a fixture that cannot be encoded is a broken test.
func mustJSON(v any) string {
	text, err := strs.JSON(v)
	if err != nil {
		panic(err)
	}
	return text
}

// db.env holds the only copy of the built-in database's password and the
// names of its container and volume; its URL names the database: on an install made before 1.0 it is
// forge, on one made since apptown. A file missing any of them, such as
// one still using the FORGE_DB_ names, is refused naming what is missing and
// left exactly as it was; an unreadable file stops with its path and is
// never replaced by new credentials; an absent one is minted.
func TestTheBuiltInDatabaseEnvFileIsReadWholeOrRefused(t *testing.T) {
	t.Parallel()
	complete := "# kept comment\nAPPTOWN_DB_URL=postgres://postgres:pw@127.0.0.1:5433/forge\nAPPTOWN_DB_PASSWORD=pw\nAPPTOWN_DB_CONTAINER=forge-db\nAPPTOWN_DB_VOLUME=forge-data-forge-db\n"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "db.env"), []byte(complete), 0o600); err != nil {
		t.Fatal(err)
	}
	url, pw, names, err := builtinDBCredentials(dir)
	if err != nil || pw != "pw" || url != "postgres://postgres:pw@127.0.0.1:5433/forge" || names != (builtinDBNames{Container: "forge-db", Volume: "forge-data-forge-db", Database: "forge"}) {
		t.Fatalf("a complete file: %q %q %+v %v", url, pw, names, err)
	}

	for _, c := range []struct{ name, body, missing string }{
		{"old names", "FORGE_DB_URL=postgres://postgres:pw@127.0.0.1:5433/forge\nFORGE_DB_PASSWORD=pw\n", "APPTOWN_DB_URL, APPTOWN_DB_PASSWORD, APPTOWN_DB_CONTAINER, APPTOWN_DB_VOLUME"},
		{"no container", strings.Replace(complete, "APPTOWN_DB_CONTAINER=forge-db\n", "", 1), "it needs APPTOWN_DB_CONTAINER"},
		{"no volume", strings.Replace(complete, "APPTOWN_DB_VOLUME=forge-data-forge-db\n", "", 1), "it needs APPTOWN_DB_VOLUME"},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "db.env"), []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := builtinDBCredentials(dir); err == nil || !strings.Contains(err.Error(), "db.env is present but incomplete; it needs") || !strings.Contains(err.Error(), c.missing) {
			t.Fatalf("%s: %v", c.name, err)
		}
		if raw, _ := os.ReadFile(filepath.Join(dir, "db.env")); string(raw) != c.body {
			t.Fatalf("%s: the file was rewritten as %q", c.name, raw)
		}
	}

	incomplete := t.TempDir()
	if err := os.WriteFile(filepath.Join(incomplete, "db.env"), []byte("APPTOWN_DB_URL=postgres://x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := builtinDBCredentials(incomplete); err == nil || !strings.Contains(err.Error(), "db.env is present but incomplete") {
		t.Fatalf("an incomplete file: %v", err)
	}
	if raw, _ := os.ReadFile(filepath.Join(incomplete, "db.env")); string(raw) != "APPTOWN_DB_URL=postgres://x\n" {
		t.Fatalf("an incomplete file was replaced: %q", raw)
	}

	unreadable := t.TempDir()
	if err := os.Mkdir(filepath.Join(unreadable, "db.env"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := builtinDBCredentials(unreadable); err == nil || !strings.Contains(err.Error(), "reading ") {
		t.Fatalf("an unreadable db.env was taken for an absent one: %v", err)
	}

	fresh := t.TempDir()
	if _, pw, names, err := builtinDBCredentials(fresh); err != nil || pw == "" || names != currentBuiltinDB {
		t.Fatalf("minting: %+v %v", names, err)
	}
	if raw, _ := os.ReadFile(filepath.Join(fresh, "db.env")); !strings.Contains(string(raw), "/apptown\nAPPTOWN_DB_PASSWORD=") || !strings.Contains(string(raw), "\nAPPTOWN_DB_CONTAINER=apptown-db\nAPPTOWN_DB_VOLUME=apptown-data-apptown-db\n") {
		t.Fatalf("a new database's names are not recorded: %q", raw)
	}
	if got, ok := readBuiltinDBNames(fresh); !ok || got != currentBuiltinDB {
		t.Fatalf("read back %+v %v", got, ok)
	}
	if got, ok := readBuiltinDBNames(dir); !ok || got != (builtinDBNames{Container: "forge-db", Volume: "forge-data-forge-db", Database: "forge"}) {
		t.Fatalf("an existing database's names read back %+v %v", got, ok)
	}
	if raw, _ := os.ReadFile(filepath.Join(fresh, "db.env")); !strings.Contains(string(raw), "\nAPPTOWN_DB_PASSWORD=") || strings.Contains(string(raw), "FORGE_") {
		t.Fatalf("minted as %q", raw)
	}
}

// A matching retained volume is not fresh storage, and a receipt is required
// before the first PostgreSQL effect, including on interrupted startup.
func TestBuiltinStoragePreservesRecordedNativeVolumeBeforeStarting(t *testing.T) {
	for _, state := range []string{"new", "running", "stopped", "container-lost", "missing-volume", "running-missing-volume", "running-wrong-mount", "replaced-volume", "running-replaced-volume", "foreign-volume", "unavailable-volume", "bad-receipt", "retained-files", "unreadable-files", "receipt-write-failed"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			pgdata := filepath.Join(dir, "pgdata")
			stamp := time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC)
			bound := builtinStorageBinding{Version: 1, Instance: "ours", Volume: currentBuiltinDB.Volume, CreatedAt: stamp}
			recorded := state != "new" && state != "retained-files" && state != "unreadable-files" && state != "receipt-write-failed"
			if recorded {
				if err := os.Mkdir(pgdata, 0o700); err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(bound)
				if err != nil {
					t.Fatal(err)
				}
				if state == "bad-receipt" {
					raw = []byte("broken")
				}
				if err := os.WriteFile(filepath.Join(pgdata, builtinStorageReceipt), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if state == "retained-files" {
				if err := os.Mkdir(pgdata, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(pgdata, "PG_VERSION"), []byte("16"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if state == "unreadable-files" {
				if err := os.WriteFile(pgdata, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			volumeExists := recorded && !strings.Contains(state, "missing-volume")
			v := builtinVolume{Name: currentBuiltinDB.Volume, Driver: "local", CreatedAt: stamp, Labels: map[string]string{dockerlabel.Instance: "ours"}}
			if strings.Contains(state, "replaced-volume") {
				v.CreatedAt = stamp.Add(time.Hour)
			}
			if state == "foreign-volume" {
				v.Labels[dockerlabel.Instance] = "other"
			}
			d := newDockerFake()
			effects := []string{}
			sawReceipt := false
			d.SetReply(func(args []string) ([]byte, error, bool) {
				if args[0] == "inspect" {
					if strings.HasPrefix(state, "running") || state == "stopped" {
						if len(args) == 2 {
							return []byte("{}"), nil, true
						}
						mount := currentBuiltinDB.Volume
						if state == "running-wrong-mount" {
							mount = "another-volume"
						}
						return []byte(map[bool]string{true: "true", false: "false"}[strings.HasPrefix(state, "running")] + "\tours\tvolume:" + mount), nil, true
					}
					return []byte("Error: No such object: " + currentBuiltinDB.Container), errors.New("exit 1"), true
				}
				if len(args) > 1 && args[0] == "volume" && args[1] == "inspect" {
					if state == "unavailable-volume" {
						return []byte("daemon unavailable"), errors.New("exit 1"), true
					}
					if !volumeExists {
						return []byte("Error: No such volume: " + currentBuiltinDB.Volume), errors.New("exit 1"), true
					}
					raw, err := json.Marshal(v)
					return raw, err, true
				}
				if len(args) > 1 && args[0] == "volume" && args[1] == "create" {
					effects = append(effects, "create")
					volumeExists = true
					if strings.Contains(strings.Join(args, " "), "device=") {
						t.Error("Darwin created a shared bind")
					}
					if state == "receipt-write-failed" {
						if err := os.Mkdir(pgdata, 0o700); err != nil {
							t.Fatal(err)
						}
						// A real path write failure, injected after empty-directory admission.
						if err := os.Mkdir(filepath.Join(pgdata, builtinStorageReceipt), 0o700); err != nil {
							t.Fatal(err)
						}
					}
					return []byte(currentBuiltinDB.Volume), nil, true
				}
				if args[0] == "run" || args[0] == "start" {
					effects = append(effects, args[0])
					b, err := readBuiltinStorage(dir, "ours", currentBuiltinDB.Volume)
					sawReceipt = err == nil && b != nil && b.CreatedAt.Equal(stamp)
					if !sawReceipt {
						t.Error("PostgreSQL effect preceded its durable storage record")
					}
					return []byte("container"), nil, true
				}
				return nil, nil, false
			})
			waited := false
			_, err := ensureBuiltinDBOnOS(t.Context(), docker.Scope{Client: d}, dir, "postgres://db", "pw", "ours", currentBuiltinDB, func(context.Context, string) error { waited = true; return nil }, "darwin")
			ok := state == "new" || state == "running" || state == "stopped" || state == "container-lost"
			if ok != (err == nil) || waited != ok {
				t.Fatalf("err=%v waited=%v effects=%v", err, waited, effects)
			}
			if !ok {
				for _, e := range effects {
					if e != "create" || state != "receipt-write-failed" {
						t.Fatalf("refused storage had effect %s", e)
					}
				}
			}
			if ok && state != "running" && !sawReceipt {
				t.Fatal("expected a recorded start")
			}
			if state == "retained-files" {
				raw, e := os.ReadFile(filepath.Join(pgdata, "PG_VERSION"))
				if e != nil || string(raw) != "16" {
					t.Fatal("retained bytes changed")
				}
			}
		})
	}
}

func TestBuiltinStorageKeepsLinuxBindAndExistingMacBind(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			owner := "th1s1nstall"
			var existing *string
			if goos == "darwin" {
				existing = &owner
			}
			d := builtinDBDaemon(existing)
			dir := t.TempDir()
			_, err := ensureBuiltinDBOnOS(t.Context(), docker.Scope{Client: d}, dir, "postgres://db", "pw", owner, currentBuiltinDB, func(context.Context, string) error { return nil }, goos)
			if err != nil {
				t.Fatal(err)
			}
			foundCreate := false
			for _, c := range d.Commands() {
				if len(c) > 1 && c[0] == "volume" && c[1] == "create" {
					foundCreate = true
					if !strings.Contains(strings.Join(c, " "), "device="+filepath.Join(dir, "pgdata")) {
						t.Fatal("Linux no longer binds its dataset")
					}
				}
			}
			if foundCreate != (goos == "linux") {
				t.Fatal("existing Mac bind was replaced")
			}
			if _, err := os.Stat(filepath.Join(dir, "pgdata", builtinStorageReceipt)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("bind gained native receipt: %v", err)
			}
		})
	}
}
