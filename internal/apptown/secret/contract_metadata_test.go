package secret

import (
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestSecretsContractMetadataAtBoot(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("app:\n  web: {image: x, public: true}\nsecrets:\n  generate: {KEY: project}\n"))
	if err != nil {
		t.Fatal(err)
	}
	// An app.yaml secrets block without a declaration file is refused at boot resolve.
	db := testDB(t)
	service := NewService(db, Config{})
	ws := t.TempDir()
	if _, err := service.Resolve(t.Context(), idstest.N(1), "p", "main", ws, c); err == nil {
		t.Fatal("secrets: without secretspec.toml must be refused")
	}
	// No declaration, no metadata: nothing happens.
	d, err := service.Resolve(t.Context(), idstest.N(1), "p", "main", ws, &appcontract.Contract{})
	if err != nil || len(d.Receipts) != 0 || d.EnvFor("web") != nil {
		t.Fatalf("no declaration: %+v %v", d, err)
	}
}
