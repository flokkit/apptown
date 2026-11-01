package instancebackup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/backuparchive"
)

// Missing provenance is legacy, but a present member must be complete and
// compatible before any dump or data file is extracted.
func TestArchiveProvenanceRefusesBeforeExtraction(t *testing.T) {
	for _, tc := range []struct {
		name, database string
		accept         bool
	}{
		{"legacy", "", true},
		{"missing schema", "", false},
		{"current", `{"engine":"postgres","serverVersionNum":160012,"dumpClientVersion":"pg_dump (PostgreSQL) 16.12"}`, true},
		{"null", `null`, false},
		{"empty", `{}`, false},
		{"partial", `{"engine":"postgres","serverVersionNum":160012}`, false},
		{"wrong engine", `{"engine":"mysql","serverVersionNum":160012,"dumpClientVersion":"pg_dump (PostgreSQL) 16.12"}`, false},
		{"unknown server", `{"engine":"postgres","serverVersionNum":0,"dumpClientVersion":"pg_dump (PostgreSQL) 16.12"}`, false},
		{"unknown client", `{"engine":"postgres","serverVersionNum":160012,"dumpClientVersion":"unknown"}`, false},
		{"future major", `{"engine":"postgres","serverVersionNum":170001,"dumpClientVersion":"pg_dump (PostgreSQL) 17.1"}`, false},
		{"older major", `{"engine":"postgres","serverVersionNum":150001,"dumpClientVersion":"pg_dump (PostgreSQL) 15.1"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]any{"format": archiveFormat, "version": archiveVersion, "schema": 167}
			if tc.name == "missing schema" {
				delete(raw, "schema")
			}
			if tc.database != "" {
				raw["database"] = json.RawMessage(tc.database)
			}
			manifest, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			var archive bytes.Buffer
			w, err := backuparchive.NewWriter(&archive, backupPassphrase)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.Bytes(manifestEntry, manifest); err != nil {
				t.Fatal(err)
			}
			if err := w.Bytes(dumpEntry, []byte("dump")); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			r, err := backuparchive.NewReader(bytes.NewReader(archive.Bytes()), backupPassphrase)
			if err != nil {
				t.Fatal(err)
			}
			m, _, err := readManifest(r)
			if (err == nil) != tc.accept {
				t.Fatalf("readManifest: %v", err)
			}
			if tc.accept {
				if (m.Database == nil) != (tc.database == "") {
					t.Fatalf("lost provenance: %+v", m.Database)
				}
				return
			}
			dst := t.TempDir()
			dump := filepath.Join(dst, "dump")
			if _, err := unpack(bytes.NewReader(archive.Bytes()), backupPassphrase, dst, dump, func(string) {}); err == nil {
				t.Fatal("unpack accepted invalid provenance")
			}
			entries, err := os.ReadDir(dst)
			if err != nil || len(entries) != 0 {
				t.Fatalf("extracted before refusal: %v %v", entries, err)
			}
		})
	}
}
