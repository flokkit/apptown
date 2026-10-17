package deploy

import (
	"testing"

	"flokkit.io/forge/internal/deploy/appcontract"
)

func TestReleaseImageCannotStandInForSeparatelyBuiltParts(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("version: 1\napp:\n  web: {build: ., public: true}\ninit:\n  - {name: migrate, build: migrations}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PreparedReleaseApp(appcontract.Prepare(c, nil), "registry.example/original:1"); err == nil {
		t.Fatal("separate build context borrowed public release image")
	}
	if c.Init[0].Build != "migrations" || c.Parts[0].Image != "" {
		t.Fatal("release preview mutated the native declaration")
	}
}
