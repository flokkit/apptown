package appcontract

import (
	"strings"
	"testing"
)

func TestStaticServerIsAnOptionalPinForCaddyTwoAlpine(t *testing.T) {
	t.Parallel()
	digest := "@sha256:" + strings.Repeat("a", 64)
	for _, image := range []string{"caddy:2.11.9-alpine" + digest, "docker.io/library/caddy:2-alpine" + digest, "caddy:2.11.9-alpine", "other:2-alpine" + digest, "caddy:3-alpine" + digest, "caddy:2" + digest} {
		_, err := Parse([]byte("staticServer: " + image + "\napp:\n  web: {static: ., public: true}\n"))
		want := image == "caddy:2.11.9-alpine"+digest || image == "docker.io/library/caddy:2-alpine"+digest
		if (err == nil) != want {
			t.Fatalf("%s: %v", image, err)
		}
	}
	if _, err := Parse([]byte("staticServer: caddy:2-alpine" + digest + "\napp:\n  web: {image: app:1, public: true}\n")); err == nil {
		t.Fatal("pin without static parts accepted")
	}
	// No pin is a valid declaration: AppTown serves the line's newest build
	// (imagelines.StaticServer). An image still unresolved when a static
	// part is about to be served is refused, never defaulted.
	c, err := Parse([]byte("app:\n  web: {static: ., public: true}\n"))
	if err != nil || c.StaticServer != "" {
		t.Fatal(c, err)
	}
	if err := ValidateStaticServer(c.StaticServer); err == nil || err.Error() != "staticServer: no server image was resolved for the static parts" {
		t.Fatal(err)
	}
}
