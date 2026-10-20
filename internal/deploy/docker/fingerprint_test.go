package docker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorktreeFingerprintSeesRepeatedEdits(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "main.js"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp1 := WorktreeFingerprint(ws)
	if err := os.WriteFile(filepath.Join(ws, "main.js"), []byte("v2 longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fp2 := WorktreeFingerprint(ws); fp2 == fp1 {
		t.Fatal("an edit must change the fingerprint")
	}
	// Derived and runtime dirs are invisible: a build output must not
	// force a reboot of the container that just produced it.
	fp3 := WorktreeFingerprint(ws)
	_ = os.MkdirAll(filepath.Join(ws, "dist"), 0o755)
	_ = os.WriteFile(filepath.Join(ws, "dist", "bundle.js"), []byte("built"), 0o644)
	_ = os.MkdirAll(filepath.Join(ws, ".apptown"), 0o755)
	_ = os.WriteFile(filepath.Join(ws, ".apptown", "preview-fingerprint"), []byte("x"), 0o644)
	if fp4 := WorktreeFingerprint(ws); fp4 != fp3 {
		t.Fatal("dist/.apptown writes must not change the fingerprint")
	}
	// The contract IS part of the print — .apptown is skipped by the walk but
	// the contract file is hashed explicitly.
	_ = os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte("app:\n  web:\n    static: .\n    public: true\n"), 0o644)
	if fp5 := WorktreeFingerprint(ws); fp5 == fp3 {
		t.Fatal("a contract change must change the fingerprint")
	}
}

// The tests gate detects the conventional entrypoints and names them; an
// unusual setup belongs in explicit step config, not cleverer detection.
