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

// db.env holds the only copy of the built-in database's password. One
// written before the AppTown rename — by a release before it (FORGE_DB_URL and
// FORGE_DB_PASSWORD, as live's was) or by the rename's first pushes
// (FORGE_DB_PASSWORD only) — is read with the same URL and password and
// rewritten once under the APPTOWN_DB_ names at 0600, comments kept; an
// incomplete or unreadable file stops with its path and is never replaced by
// new credentials; an absent one is minted under the new name.
func TestTheBuiltInDatabaseEnvFileMovesToItsAppTownNameSafely(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "db.env")
	old := "# kept comment\nAPPTOWN_DB_URL=postgres://postgres:pw@127.0.0.1:5433/forge\nFORGE_DB_PASSWORD=pw\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		url, pw, err := builtinDBCredentials(dir)
		if err != nil || pw != "pw" || url != "postgres://postgres:pw@127.0.0.1:5433/forge" {
			t.Fatalf("reading the old file: %q %q %v", url, pw, err)
		}
		raw, _ := os.ReadFile(path)
		info, _ := os.Stat(path)
		if string(raw) != "# kept comment\nAPPTOWN_DB_URL=postgres://postgres:pw@127.0.0.1:5433/forge\nAPPTOWN_DB_PASSWORD=pw\n" || info.Mode().Perm() != 0o600 {
			t.Fatalf("rewritten as %q, mode %v", raw, info.Mode().Perm())
		}
	}

	// The file forged minted from 5 Sep 2026 until the rename.
	released := t.TempDir()
	comments := "# The forge's built-in database (design/postgres-migration.md §3.9).\n" +
		"# Minted once by forged. If this file is lost while the data volume\n" +
		"# survives, reset the password inside the container (docker exec\n" +
		"# forge-db psql -U postgres -c \"ALTER USER postgres PASSWORD '...'\")\n" +
		"# and write it here.\n"
	wantURL := "postgres://postgres:ab12@127.0.0.1:5433/forge"
	if err := os.WriteFile(filepath.Join(released, "db.env"), []byte(comments+"FORGE_DB_URL="+wantURL+"\nFORGE_DB_PASSWORD=ab12\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		url, pw, err := builtinDBCredentials(released)
		if err != nil || pw != "ab12" || url != wantURL {
			t.Fatalf("reading the released file: %q %q %v", url, pw, err)
		}
		raw, _ := os.ReadFile(filepath.Join(released, "db.env"))
		info, _ := os.Stat(filepath.Join(released, "db.env"))
		if string(raw) != comments+"APPTOWN_DB_URL="+wantURL+"\nAPPTOWN_DB_PASSWORD=ab12\n" || info.Mode().Perm() != 0o600 {
			t.Fatalf("released file rewritten as %q, mode %v", raw, info.Mode().Perm())
		}
	}

	incomplete := t.TempDir()
	if err := os.WriteFile(filepath.Join(incomplete, "db.env"), []byte("APPTOWN_DB_URL=postgres://x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := builtinDBCredentials(incomplete); err == nil || !strings.Contains(err.Error(), "db.env is present but incomplete") {
		t.Fatalf("an incomplete file: %v", err)
	}
	if raw, _ := os.ReadFile(filepath.Join(incomplete, "db.env")); string(raw) != "APPTOWN_DB_URL=postgres://x\n" {
		t.Fatalf("an incomplete file was replaced: %q", raw)
	}

	unreadable := t.TempDir()
	if err := os.Mkdir(filepath.Join(unreadable, "db.env"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := builtinDBCredentials(unreadable); err == nil || !strings.Contains(err.Error(), "reading ") {
		t.Fatalf("an unreadable db.env was taken for an absent one: %v", err)
	}

	fresh := t.TempDir()
	if _, pw, err := builtinDBCredentials(fresh); err != nil || pw == "" {
		t.Fatalf("minting: %v", err)
	}
	if raw, _ := os.ReadFile(filepath.Join(fresh, "db.env")); !strings.Contains(string(raw), "\nAPPTOWN_DB_PASSWORD=") || strings.Contains(string(raw), "FORGE_") {
		t.Fatalf("minted as %q", raw)
	}
}
