package instancebackup

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/backuparchive"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

func intentManifest(t *testing.T, original string) []byte {
	t.Helper()
	raw, err := json.Marshal(Manifest{Format: ArchiveFormat, Version: ArchiveVersion, Schema: 167, DataDir: original})
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Replace(raw, []byte(`"format":`), []byte(`"unknown":{"number":1234567890123456789}, "format":`), 1)
}
func currentArchiveLayout(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "layout"), []byte("2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
func TestRestoreContinuationKeepsOriginalRootAndExactManifest(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(t.TempDir(), "original")
	raw := intentManifest(t, original)
	if err := beginRestoreIntent(root, raw); err != nil {
		t.Fatal(err)
	}
	currentArchiveLayout(t, root)
	if _, err := ReadPendingRestore(root); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("incomplete restore opened: %v", err)
	}
	if err := checkRestoreLayout(root, 2); err != nil {
		t.Fatal(err)
	}
	if err := markRestoreLoaded(root); err != nil {
		t.Fatal(err)
	}
	pending, err := ReadPendingRestore(root)
	if err != nil || pending == nil || pending.Manifest.DataDir != original || pending.Manifest.Database != nil {
		t.Fatalf("continuation: %+v %v", pending, err)
	}
	if err := refuseExistingAppTown(t.Context(), docker.Scope{}, root); err == nil || !strings.Contains(err.Error(), "already loaded") {
		t.Fatalf("loaded restore offered extraction again: %v", err)
	}
	stored, _, _, err := readRestoreIntent(root)
	if err != nil || stored.ManifestJSON != string(raw) {
		t.Fatalf("manifest bytes changed: %+v %v", stored, err)
	}
	info, err := os.Stat(filepath.Join(root, restoreIntentFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("intent permissions: %v %v", info, err)
	}
	if err := refusePendingRestore(root); err == nil {
		t.Fatal("backup admitted before restored installation finished")
	}
	if err := CompleteRestore(root, pending); err != nil {
		t.Fatal(err)
	}
	if again, err := ReadPendingRestore(root); err != nil || again != nil {
		t.Fatalf("completed intent remains: %+v %v", again, err)
	}
}
func TestRestoreCompletionRefusesChangedEvidence(t *testing.T) {
	root := t.TempDir()
	if err := beginRestoreIntent(root, intentManifest(t, root)); err != nil {
		t.Fatal(err)
	}
	currentArchiveLayout(t, root)
	if err := checkRestoreLayout(root, 2); err != nil {
		t.Fatal(err)
	}
	if err := markRestoreLoaded(root); err != nil {
		t.Fatal(err)
	}
	pending, err := ReadPendingRestore(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, restoreIntentFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CompleteRestore(root, pending); err == nil {
		t.Fatal("removed changed restore evidence")
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("changed evidence lost: %v", err)
	}
}
func TestRestoreIntentRejectsMalformedUnknownAndNonregularState(t *testing.T) {
	for _, kind := range []string{"malformed", "future", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, restoreIntentFile)
			switch kind {
			case "malformed":
				if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "future":
				if err := beginRestoreIntent(root, intentManifest(t, root)); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				raw = bytes.Replace(raw, []byte(`"format": 1`), []byte(`"format": 2`), 1)
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(root, "missing"), path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReadPendingRestore(root); err == nil {
				t.Fatal("invalid restore state treated as absent")
			}
		})
	}
}

// Restore takes only an archive in this build's layout. One from before the
// layout 2 marker (no marker, or 1) names the releases that can restore it.
func TestRestoreTakesOnlyTheCurrentArchiveLayout(t *testing.T) {
	for _, body := range []string{"missing", "1", "0", "-1", "future", "3"} {
		t.Run(body, func(t *testing.T) {
			root := t.TempDir()
			if err := beginRestoreIntent(root, intentManifest(t, root)); err != nil {
				t.Fatal(err)
			}
			if body != "missing" {
				if err := os.WriteFile(filepath.Join(root, "layout"), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := checkRestoreLayout(root, 2)
			if err == nil {
				t.Fatal("unsupported source layout accepted")
			}
			if (body == "missing" || body == "1") && !strings.Contains(err.Error(), "from 1.0.0-rc.9 to 1.0.0-rc.11") {
				t.Fatalf("an older archive is not told where to go: %v", err)
			}
		})
	}
	root := t.TempDir()
	if err := beginRestoreIntent(root, intentManifest(t, root)); err != nil {
		t.Fatal(err)
	}
	currentArchiveLayout(t, root)
	if err := checkRestoreLayout(root, 2); err != nil {
		t.Fatalf("the current layout: %v", err)
	}
}
func TestArchiveCannotReplaceRestoreControlState(t *testing.T) {
	root := t.TempDir()
	manifest := intentManifest(t, root)
	var archive bytes.Buffer
	writer, err := backuparchive.NewWriter(&archive, backupPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Bytes(manifestEntry, manifest); err != nil {
		t.Fatal(err)
	}
	if err := writer.Bytes(dataPrefix+restoreIntentFile, []byte(`{"phase":"loaded"}`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := unpack(bytes.NewReader(archive.Bytes()), backupPassphrase, root, filepath.Join(t.TempDir(), "dump"), func(string) {}); err == nil || !strings.Contains(err.Error(), "reserved restore control") {
		t.Fatalf("reserved entry did not refuse: %v", err)
	}
	state, _, found, err := readRestoreIntent(root)
	if err != nil || !found || state.Phase != "incomplete" || state.ManifestJSON != string(manifest) {
		t.Fatalf("archive replaced trusted intent: %+v %v", state, err)
	}
	for _, header := range []*tar.Header{
		{Typeflag: tar.TypeReg, Name: dataPrefix + "./" + restoreIntentFile},
		{Typeflag: tar.TypeReg, Name: dataPrefix + restoreIntentFile + "/child"},
		{Typeflag: tar.TypeSymlink, Name: "data/link", Linkname: restoreIntentFile},
		{Typeflag: tar.TypeSymlink, Name: "data/sub/link", Linkname: "../" + restoreIntentFile},
		{Typeflag: tar.TypeSymlink, Name: "data/link", Linkname: filepath.Join(root, restoreIntentFile)},
	} {
		if err := refuseRestoreControlEntry(header, root); err == nil {
			t.Fatalf("accepted reserved alias: %+v", header)
		}
	}
}
func TestNewerArchivedLayoutRefusesBeforeDatabaseEnsure(t *testing.T) {
	root := t.TempDir()
	dump := []byte("dump must never be loaded")
	digest, err := backuparchive.Sum(bytes.NewReader(dump))
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Format: ArchiveFormat, Version: ArchiveVersion, Schema: 167, DataDir: root, Dump: digest}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer, err := backuparchive.NewWriter(&archive, backupPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		name string
		raw  []byte
	}{{manifestEntry, raw}, {dumpEntry, dump}, {"data/layout", []byte("3\n")}} {
		if err := writer.Bytes(entry.name, entry.raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "archive")
	if err := os.WriteFile(archivePath, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	dk := docker.Scope{Client: dockerClientFunc(func(_ context.Context, _ string, req docker.Command) ([]byte, error) {
		if len(req.Args) == 0 || req.Args[0] != "ps" {
			t.Fatalf("restore changed Docker: %v", req.Args)
		}
		return nil, nil
	})}
	_, err = Restore(t.Context(), RestoreRequest{DataDir: root, Archive: archivePath, Passphrase: backupPassphrase, LayoutVersion: 2, Docker: dk, Database: forbiddenRestoreDatabase{t: t}})
	if err == nil || !strings.Contains(err.Error(), "layout 3 is newer") {
		t.Fatalf("layout refusal: %v", err)
	}
	if _, err := ReadPendingRestore(root); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("failed restore lost incomplete refusal: %v", err)
	}
}

func TestRestoreNeedsExplicitSupportedLayout(t *testing.T) {
	root := t.TempDir()
	if err := beginRestoreIntent(root, intentManifest(t, root)); err != nil {
		t.Fatal(err)
	}
	if err := checkRestoreLayout(root, 0); err == nil || !strings.Contains(err.Error(), "supported data layout") {
		t.Fatalf("zero layout silently accepted: %v", err)
	}
}
func TestRestoredFileBarrierDoesNotFollowArchiveSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "restored"), []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "absent"), filepath.Join(root, "external")); err != nil {
		t.Fatal(err)
	}
	if err := syncRestoredFiles(root); err != nil {
		t.Fatal(err)
	}
}
