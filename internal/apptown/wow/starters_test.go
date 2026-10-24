package wow

import (
	"io/fs"
	"testing"

	"github.com/flokkit/apptown/stock"
)

// A pack's starter is static only when its seed's contract says every part
// is static files and nothing is needed; the wizard then offers no services.
func TestStarterIsStaticReadsTheSeedContract(t *testing.T) {
	t.Parallel()
	s := New(nil, Dependencies{Stock: stock.FS, ToolLines: stock.Tools()})
	for pack, want := range map[string]bool{
		"html-game":        true,  // static: .
		"frontend-only":    true,  // static: { build: ., output: dist }
		"video-game":       true,  // static: { build: ., output: dist }
		"go-small":         false, // builds and runs a server
		"rails":            false, // needs postgres
		"conventions-only": false, // no seed
		"no-such-pack":     false,
	} {
		if got := s.StarterIsStatic(pack); got != want {
			t.Errorf("StarterIsStatic(%q) = %v, want %v", pack, got, want)
		}
	}
}

// Starter content is read from the stock the service was given, never from
// another copy: a seed and a toolchain file another stock lacks are absent.
func TestStarterContentComesFromTheGivenStock(t *testing.T) {
	t.Parallel()
	s := New(nil, Dependencies{Stock: stock.FS, ToolLines: stock.Tools()})
	seed := s.Seed("go-small")
	if seed == nil {
		t.Fatal("go-small has no seed in stock")
	}
	if _, err := fs.Stat(seed, ".apptown/app.yaml"); err != nil {
		t.Fatalf("the seed's dotfiles are missing: %v", err)
	}
	if _, err := s.Toolchain("go"); err != nil {
		t.Fatal(err)
	}
	empty := New(nil, Dependencies{Stock: fstestEmpty{}})
	if empty.Seed("go-small") != nil {
		t.Error("a seed read from a stock that lacks it")
	}
	if _, err := empty.Toolchain("go"); err == nil {
		t.Error("a toolchain read from a stock that lacks it")
	}
}

type fstestEmpty struct{}

func (fstestEmpty) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }
