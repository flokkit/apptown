package verification

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Go osv judges stdlib advisories against is the one the tree declares;
// with no declaration there is none (the agent image has no Go of its own).
//
// Kills: the agent image's Go label as a fallback for an undeclared toolchain.
func TestOSVGoOverride(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if v := goToolchainVersion(dir); v != "" {
		t.Fatalf("no pin, but a Go version: %q", v)
	}
	_ = os.WriteFile(filepath.Join(dir, "mise.toml"), []byte("[tools]\ngo = \"1.23.7\"\n"), 0o644)
	if v := goToolchainVersion(dir); v != "1.23.7" {
		t.Fatalf("the mise pin: %q", v)
	}
	if !writeOSVGoOverride(dir, "1.23.7") {
		t.Fatal("override must be written")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "osv-scanner.toml"))
	if !strings.Contains(string(b), `GoVersionOverride = "1.23.7"`) {
		t.Fatalf("config content: %s", b)
	}
	// A repo that made its own call keeps it.
	_ = os.WriteFile(filepath.Join(dir, "osv-scanner.toml"), []byte("GoVersionOverride = \"9.9\"\n"), 0o644)
	if writeOSVGoOverride(dir, "1.23.7") {
		t.Fatal("must not clobber the repo's own override")
	}
}
