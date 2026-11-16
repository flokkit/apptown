package docker

import (
	"slices"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/toolversions"
	"github.com/flokkit/apptown/stock"
)

func testImages() toolversions.Images { return toolversions.Images{DF: stock.Images().DF} }

const testStaticServer = "caddy:2.11.9-alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

var testDF = stock.Images().DF

// The preview must run the project's pin; changing Prepare to discard it
// makes this test fail even though the host itself has valid runtime images.
func TestStaticPreviewRunsTheProjectServerPin(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("staticServer: " + testStaticServer + "\napp:\n  web: {static: ., public: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeClient{reply: func(req Command) ([]byte, error) {
		if req.Args[0] == "port" {
			return []byte("127.0.0.1:45678"), nil
		}
		return nil, nil
	}}
	_, err = NewHost(Dependencies{DFImage: testDF}).startAppContainer(t.Context(), BootRequest{Allowed: AuthorisedWhenRequested, Docker: Scope{Client: fake}, Name: "preview", Workspace: t.TempDir()}, appcontract.Prepare(c, nil).PublicPart(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, args := range fake.args {
		if len(args) > 0 && args[0] == "run" && slices.Contains(args, testStaticServer) {
			found = true
		}
	}
	if !found {
		t.Fatalf("project pin absent from Docker run: %v", fake.args)
	}
	c.StaticServer = ""
	if _, err := NewHost(Dependencies{DFImage: testDF}).startAppContainer(t.Context(), BootRequest{Allowed: AuthorisedWhenRequested, Docker: Scope{Client: fake}, Name: "preview", Workspace: t.TempDir()}, appcontract.Prepare(c, nil).PublicPart(), "", nil); err == nil {
		t.Fatal("missing pin was served")
	}
}
