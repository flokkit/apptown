package forge

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/strs"
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
// names of its container and volume. A file missing any of them, such as
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
	if err != nil || pw != "pw" || url != "postgres://postgres:pw@127.0.0.1:5433/forge" || names != (builtinDBNames{Container: "forge-db", Volume: "forge-data-forge-db"}) {
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
	if raw, _ := os.ReadFile(filepath.Join(fresh, "db.env")); !strings.Contains(string(raw), "\nAPPTOWN_DB_CONTAINER=apptown-db\nAPPTOWN_DB_VOLUME=apptown-data-apptown-db\n") {
		t.Fatalf("a new database's names are not recorded: %q", raw)
	}
	if got, ok := readBuiltinDBNames(fresh); !ok || got != currentBuiltinDB {
		t.Fatalf("read back %+v %v", got, ok)
	}
	if got, ok := readBuiltinDBNames(dir); !ok || got.Container != "forge-db" || got.Volume != "forge-data-forge-db" {
		t.Fatalf("an existing database's names read back %+v %v", got, ok)
	}
	if raw, _ := os.ReadFile(filepath.Join(fresh, "db.env")); !strings.Contains(string(raw), "\nAPPTOWN_DB_PASSWORD=") || strings.Contains(string(raw), "FORGE_") {
		t.Fatalf("minted as %q", raw)
	}
}
