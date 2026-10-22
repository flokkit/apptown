package secret

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapReplacementPreservesOtherCredentials(t *testing.T) {
	t.Parallel()
	s := &Service{dataDir: t.TempDir()}
	path := filepath.Join(s.dataDir, "secrets.env")
	before := "FIRST_KEY=old\nOTHER_KEY=untouched\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, in := range [][2]string{{"FIRST_KEY\nINJECTED", "bad"}, {"FIRST_KEY", "bad\nINJECTED=value"}} {
		if err := s.SetBootstrap(in[0], in[1]); err == nil {
			t.Fatal("multi-line credential accepted")
		}
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != before {
		t.Fatal("rejected credential changed the file")
	}
	if err := s.SetBootstrap("FIRST_KEY", "replacement"); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "OTHER_KEY=untouched\nFIRST_KEY=replacement\n" {
		t.Fatal("replacement did not preserve the other credential")
	}
}
