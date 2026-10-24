package docker

import (
	"testing"

	"github.com/flokkit/apptown/internal/runtimeimages"
)

// A static part is served by the static server of the tool versions the
// process loaded, unless the host was given one. Not parallel: it sets the
// process's images, and restores them.
func TestAStaticPartIsServedByTheLoadedStaticServer(t *testing.T) {
	prev := runtimeimages.Images{Sh: runtimeimages.Sh(), DF: runtimeimages.DF(), StaticServer: runtimeimages.StaticServer(), PrivateDocker: runtimeimages.PrivateDocker()}
	t.Cleanup(func() { runtimeimages.Set(prev) })
	loaded := prev
	loaded.StaticServer = "example.invalid/caddy@sha256:" + "0000000000000000000000000000000000000000000000000000000000000003"
	runtimeimages.Set(loaded)
	if got := (&Host{}).staticServer(); got != loaded.StaticServer {
		t.Fatalf("served by %q, not the loaded %q", got, loaded.StaticServer)
	}
	if got := (&Host{deps: Dependencies{StaticServer: "given"}}).staticServer(); got != "given" {
		t.Fatalf("a host given a server used %q", got)
	}
}
