package secret

import (
	"context"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/secret/internal/secretsql"
)

func TestInternalStoreObfuscates(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	service := NewService(db, Config{})
	v1, err := service.Vault.Put(t.Context(), "env:p:main", "TOKEN", "hunter2", "bls")
	if err != nil || v1 != 1 {
		t.Fatalf("put: %d %v", v1, err)
	}
	v2, _ := service.Vault.Put(t.Context(), "env:p:main", "TOKEN", "hunter3", "bls")
	if v2 != 2 {
		t.Fatalf("second put is version 2, got %d", v2)
	}
	got, ver, ok := service.Stored(t.Context(), "env:p:main", "TOKEN")
	if !ok || got != "hunter3" || ver != 2 {
		t.Fatalf("get: %q v%d ok=%v", got, ver, ok)
	}
	// The stored form is not the plaintext, and the key is one per instance.
	row, err := queries(db).LatestSecretValue(context.Background(), secretsql.LatestSecretValueParams{Scope: "env:p:main", Name: "TOKEN"})
	if err != nil || row.Version != 2 {
		t.Fatalf("stored row: %+v %v", row, err)
	}
	if stored := row.Value; stored == "hunter3" || strings.Contains(stored, "hunter") {
		t.Fatalf("value stored in the clear: %q", stored)
	}
	k1, err := service.Vault.Key(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = service.Vault.Put(t.Context(), "env:p:main", "OTHER", "x", "bls")
	if k2, _ := service.Vault.Key(t.Context()); len(k1) != 32 || string(k2) != string(k1) {
		t.Fatal("the obfuscation key must be minted once and kept")
	}
	if _, err := service.Vault.Put(t.Context(), "env:p:main", "EMPTY", "  ", "bls"); err == nil {
		t.Fatal("an empty value is not a secret")
	}

}
