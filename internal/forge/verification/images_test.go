package verification

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/stock"
)

func TestCheckEnvironmentIsDerivedFromTheDeclaration(t *testing.T) {
	t.Parallel()
	catalog := dependency.NewCatalog(testDB(t), nil)
	if _, _, err := catalog.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	s := Scanners{dependencies: catalog}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secretspec.toml"), []byte("[project]\nname = \"x\"\nrevision = \"1.0\"\n\n[profiles.default]\nSECRET_KEY = { description = \"key\", generate = true }\nSTRIPE_KEY = { description = \"stripe\", required = true }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".forge", "app.yaml"), []byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres:\n    version: \"16\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, e := range s.checkEnvironment(t.Context(), dir) {
		k, v, _ := strings.Cut(e, "=")
		env[k] = v
	}
	real, _ := secret.GenerateValue()
	if len(env["SECRET_KEY"]) != len(real) || len(env["STRIPE_KEY"]) != len(real) {
		t.Fatalf("declared secrets are minted like real ones: %v", env)
	}
	if !strings.HasPrefix(env["DATABASE_URL"], "postgres://") || !strings.Contains(env["DATABASE_URL"], "check.forge.local") {
		t.Fatalf("a declared need brings its emulator's connection env: %q", env["DATABASE_URL"])
	}
	if env["FORGE_ORIGINS"] == "" {
		t.Fatal("FORGE_ORIGINS is set as it is in every environment")
	}
	if _, invented := env["DEBUG"]; invented {
		t.Fatal("nothing AppTown does not set in a deployment is set here")
	}
	// No declaration: only what the forge sets everywhere.
	bare := t.TempDir()
	if got := s.checkEnvironment(t.Context(), bare); len(got) != 1 || !strings.HasPrefix(got[0], "FORGE_ORIGINS=") {
		t.Fatalf("an app declaring nothing gets nothing invented: %v", got)
	}
}
