package main

import (
	"os"
	"path/filepath"
	"testing"
)

// `config validate` sends the secrets declaration the contract names
// (secrets.spec), not always secretspec.toml; a contract that does not parse
// is sent alone.
func TestConfigValidateSendsTheSecretsSpecTheContractDeclares(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("secretspec.toml", "root spec")
	write("config/secrets.toml", "declared spec")
	const app = "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"
	contract := filepath.Join(root, ".apptown", "app.yaml")
	write(".apptown/app.yaml", app+"secrets: { spec: config/secrets.toml }\n")
	body, err := validateBody(contract)
	if err != nil || body["secretSpec"] != "declared spec" {
		t.Fatalf("a declared spec: %q %v", body["secretSpec"], err)
	}
	write(".apptown/app.yaml", app)
	if body, err := validateBody(contract); err != nil || body["secretSpec"] != "root spec" {
		t.Fatalf("the default spec: %q %v", body["secretSpec"], err)
	}
	write(".apptown/app.yaml", "not: [a contract")
	if body, err := validateBody(contract); err != nil || len(body) != 1 {
		t.Fatalf("a contract that does not parse is sent alone: %v %v", body, err)
	}
}
