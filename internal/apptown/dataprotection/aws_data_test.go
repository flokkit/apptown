package dataprotection

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/awsecs"
	"github.com/flokkit/apptown/internal/deploy/s3bucket/s3fake"
)

// The deletion guard reads the set's own stored manifest and empty proofs.
// Neither an uploaded object alone nor a provider exclusion asserts coverage.
func TestAWSFinalSetRequiresVerifiedManifestDumpCoverageAndEmptyProof(t *testing.T) {
	w := newFlyDataWorld(t)
	w.attach(t)
	manifest := awsecs.DatabaseManifest{Instance: "instance", Databases: []string{"rdsadmin", "app", "postgres", "extra"}, ProviderOwned: []string{awsecs.ProviderOwnedDatabase}, Empty: map[string]bool{"app": false, "postgres": true, "extra": false}}
	raw, _ := json.Marshal(manifest)
	item := func(kind, db, file string, body []byte) BackupItem {
		return BackupItem{Kind: kind, Instance: "instance", Database: db, File: file, Bytes: int64(len(body)), SHA256: fmt.Sprintf("%x", sha256.Sum256(body))}
	}
	b := &Backup{Status: BackupOK, Store: StoreBackupStore, Path: "set", StoreEndpoint: w.store.API.URL, StoreBucket: "copies", Items: []BackupItem{item("manifest", "", "manifest.json", raw), item("need", "app", "app.dump", []byte("dump")), item("empty", "postgres", "postgres.empty", []byte("empty\n"))}}
	w.store.Put("copies", "set/manifest.json", s3fake.Object{Body: raw})
	w.store.Put("copies", "set/postgres.empty", s3fake.Object{Body: []byte("empty\n")})
	if _, err := w.s.readAWSManifest(t.Context(), b, "instance"); err == nil {
		t.Fatal("uncovered extra database allowed deletion")
	}
	b.Items = append(b.Items, item("need", "extra", "extra.dump", []byte("extra dump")))
	if _, err := w.s.readAWSManifest(t.Context(), b, "instance"); err != nil {
		t.Fatalf("empty postgres should pass: %v", err)
	}
	w.store.Put("copies", "set/postgres.empty", s3fake.Object{Body: []byte("not empty\n")})
	if _, err := w.s.readAWSManifest(t.Context(), b, "instance"); err == nil {
		t.Fatal("changed empty proof allowed deletion")
	}
	w.store.Put("copies", "set/postgres.empty", s3fake.Object{Body: []byte("empty\n")})
	manifest.Databases = manifest.Databases[:3]
	changed, _ := json.Marshal(manifest)
	w.store.Put("copies", "set/manifest.json", s3fake.Object{Body: changed})
	if _, err := w.s.readAWSManifest(t.Context(), b, "instance"); err == nil {
		t.Fatal("replaced manifest allowed deletion")
	}
}
