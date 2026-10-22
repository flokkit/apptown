package verification

import (
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// An image scan's receipt says what it covers: the public part's image and
// how it was obtained, never the release's digest, and every other part
// and init step, by name, as not scanned.
//
// Kills: a receipt that read as covering the whole release; a private
// static part left off the list; a cache hit described as a build.
func TestAnImageScanNamesWhatItScannedAndWhatItDidNot(t *testing.T) {
	t.Parallel()
	parse := func(y string) *appcontract.Contract {
		t.Helper()
		c, err := appcontract.Parse([]byte(y))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	built := parse("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n  worker:\n    build: ./worker\n    command: [\"work\"]\ninit:\n  - {name: migrate, build: ., command: [\"migrate\"]}\n")
	scanned, notScanned := imageScanScope(built, "")
	if scanned["part"] != "web" || !strings.Contains(scanned["how"], "built from this commit for the checks") {
		t.Fatalf("scanned: %v", scanned)
	}
	if !slices.Equal(notScanned, []string{"part worker", "init step migrate"}) {
		t.Fatalf("not scanned: %v", notScanned)
	}
	if scanned, _ := imageScanScope(built, cachedImageNote); scanned["how"] != cachedImageNote || strings.Contains(scanned["how"], "built from this commit for the checks;") {
		t.Fatalf("a cache hit says only that it was cached: %v", scanned)
	}
	pulled := parse("app:\n  web:\n    image: registry.example/app:1\n    ports: [\"8080\"]\n    public: true\n  assets:\n    static: site\n    ports: [\"8081\"]\n")
	if scanned, notScanned := imageScanScope(pulled, ""); scanned["how"] != "pulled by its reference registry.example/app:1" || !slices.Equal(notScanned, []string{"part assets"}) {
		t.Fatalf("a pulled image beside a private static part: %v %v", scanned, notScanned)
	}
	// A static public part is skipped; the other images are still named.
	static := parse("app:\n  site:\n    static: public\n    public: true\n  worker:\n    image: w:1\n    command: [\"work\"]\ninit:\n  - {name: migrate, image: w:1, command: [\"migrate\"]}\n")
	if got := otherImages(static); !slices.Equal(got, []string{"part worker", "init step migrate"}) {
		t.Fatalf("beside a static public part: %v", got)
	}
	if !strings.Contains(staticPublicNote, "skipped here") || strings.Contains(staticPublicNote, "no app image") {
		t.Fatalf("the static note says only what is known: %q", staticPublicNote)
	}
}
