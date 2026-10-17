package agentenv

import (
	"strings"
	"testing"
)

// The registry adapters produce the env or config a build step reads, from
// the instance's mirror base — no hard-coded host.
func TestRegistryAdapters(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"npm", "pypi", "cargo", "maven", "nuget", "rubygems", "go-proxy"} {
		if registryAdapterFor(format) == nil {
			t.Errorf("no adapter for %s", format)
		}
	}
	mvn := registryAdapterFor("maven")
	files := mvn.Files("https://mirror.example.com/repository/maven/")
	if len(files) != 1 || !strings.Contains(files[0].Content, "mirror.example.com") {
		t.Fatalf("maven settings must carry the mirror: %+v", files)
	}
	ng := registryAdapterFor("nuget")
	if env := ng.Env("https://x/"); len(env) == 0 || !strings.Contains(env[0], "RestoreConfigFile") {
		t.Fatalf("nuget env: %v", env)
	}
}
