package agentenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/execfile"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/integration"
)

func TestAmbientPreparationPreservesProjectConfigurationAndIsolatesInstalls(t *testing.T) {
	t.Parallel()
	bound := map[string]integration.BoundRegistry{"npm": {Name: "npm-mirror", URL: "https://mirror.invalid/npm/"}}
	root := t.TempDir()
	var installs []string
	for _, name := range []string{"first", "second"} {
		ws := filepath.Join(root, name)
		if err := os.MkdirAll(ws, 0755); err != nil {
			t.Fatal(err)
		}
		projectConfig := filepath.Join(ws, "mise.toml")
		const original = "[tools]\nnode = '22'\n"
		if err := os.WriteFile(projectConfig, []byte(original), 0644); err != nil {
			t.Fatal(err)
		}
		env := AmbientFor(bound, ws)
		var install string
		for _, value := range env.Env {
			if strings.HasPrefix(value, "MISE_DATA_DIR=") {
				install = strings.TrimPrefix(value, "MISE_DATA_DIR=")
			}
			if strings.HasPrefix(value, "PATH=") && !strings.HasPrefix(value, "PATH=/opt/apptown/bin:") {
				t.Fatal("toolchain can shadow the agents' CLI")
			}
		}
		if install == "" {
			t.Fatal("workspace has no isolated install directory")
		}
		installs = append(installs, install)
		got, err := os.ReadFile(projectConfig)
		if err != nil || string(got) != original {
			t.Fatal("ambient preparation changed project configuration")
		}
		var generated string
		for _, f := range env.Files {
			if f.Path == "/etc/mise/config.toml" {
				generated = string(f.Content)
			}
		}
		if !strings.Contains(generated, `disable_backends = ["ubi"]`) {
			t.Fatal("missing system-layer toolchain restrictions")
		}
		// Generated beside the tree: nothing is written into the workspace.
		if _, err := os.Lstat(filepath.Join(ws, ".apptown")); !os.IsNotExist(err) {
			t.Fatalf("ambient preparation wrote into the workspace: %v", err)
		}
	}
	if installs[0] == installs[1] {
		t.Fatal("two workspaces share mutable tool installations")
	}
}

// The generated files are presented at the same container paths as when
// they were written into the tree and mounted from there: the system layer
// of mise, the forge's CARGO_HOME, and /etc/apptown for maven.
func TestGeneratedFilesKeepTheirContainerPaths(t *testing.T) {
	t.Parallel()
	bound := map[string]integration.BoundRegistry{
		"npm":   {Name: "npm", URL: "https://mirror.invalid/npm/"},
		"cargo": {Name: "cargo", URL: "https://mirror.invalid/cargo/"},
		"maven": {Name: "maven", URL: "https://mirror.invalid/maven/"},
	}
	amb := AmbientFor(bound, t.TempDir())
	var paths []string
	for _, f := range amb.Files {
		paths = append(paths, f.Path)
		if f.Mode != 0o644 || len(f.Content) == 0 {
			t.Errorf("%s: mode %v, %d bytes", f.Path, f.Mode, len(f.Content))
		}
	}
	if got, want := strings.Join(paths, " "), "/etc/mise/config.toml /etc/apptown/maven-settings.xml /apptown-cache/cargo/config.toml"; got != want {
		t.Fatalf("paths %q, want %q", got, want)
	}
	if err := execution.ValidFiles(amb.Files); err != nil {
		t.Fatal(err)
	}
}

// A project's .apptown that is a link out of its tree reaches nothing: the
// configuration is generated as bytes and written by the runner in a folder
// of its own, so the link's target is never written.
func TestAProjectsForgeLinkReachesNothing(t *testing.T) {
	t.Parallel()
	ws, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(ws, ".apptown")); err != nil {
		t.Fatal(err)
	}
	amb := AmbientFor(map[string]integration.BoundRegistry{"npm": {Name: "npm", URL: "https://mirror.invalid/npm/"}}, ws)
	docker := filepath.Join(t.TempDir(), "docker")
	if err := execfile.Write(docker, []byte("#!/bin/sh\n[ \"$1\" = inspect ] && echo false\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := Execute(execution.Runner{DockerBinary: docker, Scratch: t.TempDir()}, context.Background(), execution.Docker, execution.Request{
		Workspace: ws, Argv: []string{"true"}, Image: "alpine:3", Name: "forge-test", Timeout: time.Minute, Files: amb.Files}, time.Now())
	if !out.Exited0() {
		t.Fatalf("%+v", out)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("the link's target was written: %v", entries)
	}
}

// A forge with no registry bound still installs what a project's mise.toml
// declares: the toolchain layer is the declaration's, not the mirrors'.
// Without mirrors a declared Ruby would otherwise never be installed.
func TestTheToolchainLayerNeedsNoRegistry(t *testing.T) {
	t.Parallel()
	amb := AmbientFor(nil, "/work/step-9-a1")
	if amb.Bound != nil {
		t.Fatalf("nothing is bound, yet the receipt names %v", amb.Bound)
	}
	var config, data, cache, path string
	for _, f := range amb.Files {
		if f.Path == "/etc/mise/config.toml" {
			config = string(f.Content)
		}
	}
	for _, v := range amb.Env {
		switch {
		case strings.HasPrefix(v, "MISE_DATA_DIR="):
			data = strings.TrimPrefix(v, "MISE_DATA_DIR=")
		case strings.HasPrefix(v, "MISE_CACHE_DIR="):
			cache = strings.TrimPrefix(v, "MISE_CACHE_DIR=")
		case strings.HasPrefix(v, "PATH="):
			path = v
		}
	}
	if !strings.Contains(config, "python.compile = false") || data == "" || !strings.Contains(path, data+"/shims") {
		t.Fatalf("no toolchain layer without a registry: config %q, data %q, %s", config, data, path)
	}
	// mise's download cache is where a worker mounts its shared tool cache;
	// the installs are not in it, so no project's installed tools are
	// another's.
	if cache != execution.ToolCacheMount || strings.HasPrefix(data, cache+"/") {
		t.Fatalf("mise's cache %q (the shared mount is %s), its installs %q", cache, execution.ToolCacheMount, data)
	}
}

// Maven's own tarball is fetched from Maven Central, not archive.apache.org
// (which throttles and refuses connections, failing a java tests gate):
// through the bound Maven repository when there is one, and
// left to the Apache dist mirror when that is bound. A refused download is
// tried again before the install fails.
func TestMavenItselfComesFromCentralUnlessAnApacheMirrorIsBound(t *testing.T) {
	t.Parallel()
	config := func(bound map[string]integration.BoundRegistry) string {
		for _, f := range AmbientFor(bound, "/work/x").Files {
			if f.Path == "/etc/mise/config.toml" {
				return string(f.Content)
			}
		}
		t.Fatal("no mise config")
		return ""
	}
	const rule = `"regex:^https://archive\\.apache\\.org/dist/maven/maven-3/([^/]+)/binaries/(apache-maven-[^/]+)$" = `
	none := config(nil)
	if !strings.Contains(none, rule+`"https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/$1/$2"`) {
		t.Fatalf("no registry bound: maven not routed to Central:\n%s", none)
	}
	if !strings.Contains(none, "http_retries = 3") {
		t.Fatalf("downloads are not retried:\n%s", none)
	}
	if strings.Count(none, "[settings.url_replacements]") != 1 {
		t.Fatalf("one url_replacements table:\n%s", none)
	}
	mirrored := config(map[string]integration.BoundRegistry{"maven": {Name: "m", URL: "https://mirror.invalid/maven/"}})
	if !strings.Contains(mirrored, rule+`"https://mirror.invalid/maven/org/apache/maven/apache-maven/$1/$2"`) {
		t.Fatalf("a bound Maven repository is not used:\n%s", mirrored)
	}
	dist := config(map[string]integration.BoundRegistry{"apache-dist": {Name: "a", URL: "https://dist.invalid/"}})
	if strings.Contains(dist, "apache-maven-") || strings.Count(dist, "[settings.url_replacements]") != 1 {
		t.Fatalf("a bound Apache mirror keeps the route:\n%s", dist)
	}
}
