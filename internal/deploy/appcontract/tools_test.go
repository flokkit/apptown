package appcontract

import (
	"slices"
	"strings"
	"testing"
)

// A part names what it is built with, from its folder; the folders that are
// not the app's code are listed beside the parts and never hold one.
func TestPartToolsAndNotAppCode(t *testing.T) {
	t.Parallel()
	c, err := Parse([]byte(`version: 2
app:
  api: {build: ./api, ports: ["8080"], public: true, tools: [go, node]}
  site: {static: docs-site}
notAppCode: [docs/tools, ./e2e/]
`))
	if err != nil {
		t.Fatal(err)
	}
	api := c.Parts[0]
	if api.Name != "api" || !slices.Equal(api.Tools, []string{"go", "node"}) || api.Folder() != "api" {
		t.Errorf("api: %+v folder %q", api.Tools, api.Folder())
	}
	if c.Parts[1].Folder() != "docs-site" || len(c.Parts[1].Tools) != 0 {
		t.Errorf("site: %q %v", c.Parts[1].Folder(), c.Parts[1].Tools)
	}
	if !slices.Equal(c.NotAppCode, []string{"docs/tools", "e2e"}) {
		t.Errorf("notAppCode: %v", c.NotAppCode)
	}
	for _, tc := range []struct{ yaml, want string }{
		{`app: {web: {image: nginx:1, public: true, tools: [go]}}`, "an image: part has none"},
		{`app: {web: {build: ., public: true, tools: [Go]}}`, "not a tool name"},
		{`app: {web: {build: ., public: true, tools: [go, go]}}`, "twice"},
		{"app: {web: {build: ., public: true}}\nnotAppCode: [../elsewhere]", "inside the repository"},
		{"app: {web: {build: ., public: true}}\nnotAppCode: [/abs]", "inside the repository"},
		{"app: {web: {build: ., public: true}}\nnotAppCode: [.]", "inside the repository"},
		{"app: {web: {build: services/web, public: true}}\nnotAppCode: [services]", "holds part web's folder"},
		{"app: {web: {build: services/web, public: true}}\nnotAppCode: [services/web]", "holds part web's folder"},
	} {
		if _, err := Parse([]byte("version: 2\n" + tc.yaml)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.yaml, err, tc.want)
		}
	}
}
