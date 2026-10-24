package docker

import (
	"testing"

	"github.com/flokkit/apptown/internal/toolversions"
	"github.com/flokkit/apptown/stock"
)

// A static part is served by the static server its caller's snapshot gave
// the host; a host given none refuses, naming why, rather than guess one.
func TestAStaticPartIsServedByTheStaticServerTheHostWasGiven(t *testing.T) {
	t.Parallel()
	if got, err := (&Host{deps: Dependencies{StaticServer: "given"}}).staticServer(); err != nil || got != "given" {
		t.Fatalf("a host given a server used %q (%v)", got, err)
	}
	if got, err := (&Host{}).staticServer(); err == nil {
		t.Fatalf("a host given no server used %q", got)
	}
}

// testImages are a host's images in tests: the stock tool versions' static
// server and df, and no chown image, which skips handing volumes over.
func testImages() toolversions.Images {
	im := stock.Images()
	return toolversions.Images{StaticServer: im.StaticServer, DF: im.DF}
}

// The stock tool versions' static server and df, which test hosts are
// given as a caller's snapshot would give them.
var testStaticServer, testDF = stock.Images().StaticServer, stock.Images().DF
