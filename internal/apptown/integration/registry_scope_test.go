package integration

import (
	"strings"
	"testing"
)

func TestRegistryDestinationUsesOCIAuthority(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ repo, connector, want string }{
		{"registry.example/team/app", "registry.example", "registry.example"},
		{"Registry.Example/team/app", "registry.example/", "registry.example"},
		{"registry.example:5443/team/app", "registry.example:5443", "registry.example:5443"},
		{"[::1]:5000/team/app", "[::1]:5000", "[::1]:5000"},
		{"docker.io/team/app", "index.docker.io", "index.docker.io"},
		{"DOCKER.IO/team/app", "docker.io", "index.docker.io"},
		{"team/app", "docker.io", "index.docker.io"},
		{"alpine", "", "index.docker.io"},
		{"localhost:5000/team/app", "localhost:5000", "localhost:5000"},
		{"registry.example/team/app", "other.example", ""},
		{"registry.example:5443/team/app", "registry.example:5444", ""},
		{"registry.example/team/app", "registry.example:443", ""},
		{"localhost/team/app", "localhost", "localhost"},
		{"localhost/team/app", "docker.io", ""},
		{"registry/team/app", "registry", ""},
		{"registry.example/team/app", "https://registry.example", ""},
		{"registry.example/team/app", "user:private-canary@registry.example", ""},
		{"registry.example/team/app", "registry.example/path", ""},
		{"registry.example/team/app", "registry.example?q=x", ""},
		{"registry.example/team/app", "registry.example#x", ""},
		{"registry.example/team/app:tag", "registry.example", ""},
		{"registry.example/team/app@sha256:" + strings.Repeat("a", 64), "registry.example", ""},
		{"https://user:private-canary@registry.example/team/app", "registry.example", ""},
	} {
		t.Run(c.repo+"/"+c.connector, func(t *testing.T) {
			got, err := registryDestination(c.repo, c.connector)
			if c.want == "" {
				if err == nil || got != "" {
					t.Fatalf("unsafe registry accepted: %q %v", got, err)
				}
				if strings.Contains(err.Error(), "private-canary") {
					t.Fatal("error exposed malformed credential")
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("destination=%q err=%v want=%q", got, err, c.want)
			}
		})
	}
}
