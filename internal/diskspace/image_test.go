package diskspace

import (
	"slices"
	"testing"

	"github.com/flokkit/apptown/internal/runtimeimages"
)

// A disk read through Docker runs df in the df image of the tool versions
// the process loaded. Not parallel: it sets the process's images, and
// restores them.
func TestADiskReadRunsInTheLoadedImage(t *testing.T) {
	prev := runtimeimages.Images{Sh: runtimeimages.Sh(), DF: runtimeimages.DF(), StaticServer: runtimeimages.StaticServer(), PrivateDocker: runtimeimages.PrivateDocker()}
	t.Cleanup(func() { runtimeimages.Set(prev) })
	loaded := prev
	loaded.DF = "example.invalid/df@sha256:" + "0000000000000000000000000000000000000000000000000000000000000002"
	runtimeimages.Set(loaded)
	if args := DockerDFArgs("/var/lib/docker"); !slices.Contains(args, loaded.DF) {
		t.Fatalf("df ran as %v, not in %q", args, loaded.DF)
	}
}
