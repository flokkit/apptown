package agentenv

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/trust"
)

// Package registries & mirrors (design/subsystems/registries.md): one binding = ONE
// local registry/mirror endpoint; the format enum is the contract consumers
// program against. Providers (Nexus, Artifactory) exist only as catalog
// presets. Consumers materialize bindings into the AMBIENT BUILD
// ENVIRONMENT — env vars + generated client config files — through the
// per-format adapter table below. The executor stays generic: everything
// threads through ExecRequest.Env/Mounts.
//
// File-ownership rule: claim a LAYER, not a file. AppTown owns the system/
// ambient layer of each client's config hierarchy (an AppTown-owned CARGO_HOME
// inside the cache volume, /etc/mise/config.toml); project files win on
// conflict. npm/go/pip/uv are env-only.

// A generated client-config file: generated as bytes and presented
// read-only at ContainerPath (the SYSTEM layer of that client's config
// hierarchy — never image/user/project files).
type generatedFile struct{ Name, ContainerPath, Content string }

type registryAdapter struct {
	Format    string
	Title     string
	Layer     string                    // "packages" (universal package-manager traffic) | "toolchain" (mise/rustup supply line)
	Mechanism string                    // how the ambient env delivers it — for the generated steering section
	Env       func(url string) []string // real container env (the only hook that reaches install subprocesses)
	MiseConf  func(url string) string   // fragment merged into the generated /etc/mise/config.toml
	CargoConf func(url string) string   // fragment for the generated $CARGO_HOME/config.toml
	Files     func(url string) []generatedFile
	testPath  string // probe path appended to the bound URL (known-small artifact)
}

var registryAdapters = []registryAdapter{
	{
		// Consumed by the per-step private docker daemons (dind.go), not by
		// step env: dockerd --registry-mirror. Pull-through cache for Hub.
		Format: "docker-hub", Title: "Docker Hub mirror", Layer: "images",
		Mechanism: "dockerd `--registry-mirror` on agent daemons",
		testPath:  "/v2/",
	},
	{
		Format: "go-proxy", Title: "Go module proxy", Layer: "packages",
		Mechanism: "`GOPROXY` env",
		Env:       func(u string) []string { return []string{"GOPROXY=" + u} },
		testPath:  "/github.com/pkg/errors/@v/list",
	},
	{
		Format: "npm", Title: "npm registry", Layer: "packages",
		Mechanism: "`NPM_CONFIG_REGISTRY` env",
		Env:       func(u string) []string { return []string{"NPM_CONFIG_REGISTRY=" + u} },
		testPath:  "/-/ping",
	},
	{
		Format: "pypi", Title: "PyPI index", Layer: "packages",
		Mechanism: "`UV_DEFAULT_INDEX` / `PIP_INDEX_URL` env",
		Env: func(u string) []string {
			return []string{"UV_DEFAULT_INDEX=" + u, "PIP_INDEX_URL=" + u}
		},
		// pipx backend: version listing AND the derived UV_INDEX for mise's
		// uv subprocess come from this one setting.
		MiseConf: func(u string) string {
			return fmt.Sprintf("pipx.registry_url = %q\n", strings.TrimSuffix(u, "/")+"/{}/")
		},
		testPath: "/pip/",
	},
	{
		Format: "cargo", Title: "Cargo sparse index", Layer: "packages",
		Mechanism: "generated `$CARGO_HOME/config.toml` (source replacement; read-only)",
		// cargo ignores env for source replacement — generated config file only.
		CargoConf: func(u string) string {
			return fmt.Sprintf("[source.crates-io]\nreplace-with = \"apptown-local\"\n\n[source.apptown-local]\nregistry = %q\n",
				"sparse+"+strings.TrimSuffix(u, "/")+"/")
		},
		testPath: "/config.json",
	},
	{
		Format: "github-releases", Title: "GitHub releases mirror", Layer: "toolchain",
		Mechanism: "mise url_replacements in `/etc/mise/config.toml`",
		MiseConf: func(u string) string {
			b := strings.TrimSuffix(u, "/")
			return fmt.Sprintf("[settings.url_replacements]\n"+
				"\"regex:^https://github\\\\.com/([^/]+)/([^/]+)/(releases/download/.+)$\" = %q\n"+
				"\"regex:^https://github\\\\.com/([^/]+)/([^/]+)/(archive/.+)$\" = %q\n",
				b+"/$1/$2/$3", b+"/$1/$2/$3")
		},
		testPath: "/jdx/mise/releases/download/v2026.8.12/SHASUMS256.txt",
	},
	{
		Format: "gitlab-releases", Title: "GitLab releases mirror", Layer: "toolchain",
		Mechanism: "mise url_replacements in `/etc/mise/config.toml`; agent-image builds",
		MiseConf: func(u string) string {
			b := strings.TrimSuffix(u, "/")
			return fmt.Sprintf("[settings.url_replacements]\n"+
				"\"regex:^https://gitlab\\\\.com/(.+/-/releases/.+)$\" = %q\n"+
				"\"regex:^https://gitlab\\\\.com/(.+/-/archive/.+)$\" = %q\n",
				b+"/$1", b+"/$1")
		},
		testPath: "/gitlab-org/cli/-/releases/v1.55.0/downloads/glab_1.55.0_linux_arm64.deb",
	},
	{
		Format: "node-dist", Title: "Node.js dist mirror", Layer: "toolchain",
		Mechanism: "mise `node.mirror_url` in `/etc/mise/config.toml`",
		MiseConf: func(u string) string {
			return fmt.Sprintf("node.mirror_url = %q\n", strings.TrimSuffix(u, "/")+"/")
		},
		testPath: "/latest/SHASUMS256.txt",
	},
	{
		Format: "golang-dist", Title: "Go toolchain mirror", Layer: "toolchain",
		Mechanism: "mise `go.download_mirror` in `/etc/mise/config.toml`",
		MiseConf: func(u string) string {
			return fmt.Sprintf("go.download_mirror = %q\n", strings.TrimSuffix(u, "/"))
		},
		testPath: "/go1.24.4.src.tar.gz",
	},
	{
		Format: "rust-dist", Title: "Rust toolchain dist", Layer: "toolchain",
		Mechanism: "`RUSTUP_DIST_SERVER` / `RUSTUP_UPDATE_ROOT` env",
		// rustup reads both from env; mise's rust backend drives rustup as a
		// subprocess, so real container env (our injection point) reaches it.
		// /dist and /rustup live on the one upstream host, so one mirror
		// covers toolchains AND rustup-init.
		Env: func(u string) []string {
			b := strings.TrimSuffix(u, "/")
			return []string{"RUSTUP_DIST_SERVER=" + b, "RUSTUP_UPDATE_ROOT=" + b + "/rustup"}
		},
		testPath: "/rustup/release-stable.toml",
	},
	{
		Format: "maven", Title: "Maven repository", Layer: "packages",
		Mechanism: "`MAVEN_ARGS` → generated `/etc/apptown/maven-settings.xml`",
		// Maven ignores env for mirrors; MAVEN_ARGS (maven ≥3.9) points every
		// invocation at the generated settings file — a project's own -s flag
		// or .mvn/maven.config wins by appearing later on the command line.
		Env: func(u string) []string { return []string{"MAVEN_ARGS=--settings /etc/apptown/maven-settings.xml"} },
		Files: func(u string) []generatedFile {
			const tmpl = `<!-- GENERATED by AppTown from registry bindings - do not edit. -->
<settings xmlns="http://maven.apache.org/SETTINGS/1.0.0">
  <mirrors>
    <mirror>
      <id>apptown-local</id>
      <mirrorOf>central</mirrorOf>
      <url>%s</url>
    </mirror>
  </mirrors>
</settings>
`
			return []generatedFile{{
				Name: "maven-settings.xml", ContainerPath: "/etc/apptown/maven-settings.xml",
				Content: fmt.Sprintf(tmpl, strings.TrimSuffix(u, "/")),
			}}
		},
		testPath: "/org/slf4j/slf4j-api/maven-metadata.xml",
	},
	{
		Format: "nuget", Title: "NuGet feed", Layer: "packages",
		Mechanism: "`RestoreConfigFile` → generated `/etc/apptown/NuGet.Config`",
		// NuGet has no registry env var, but MSBuild turns every environment
		// variable into a property, and RestoreConfigFile is the restore
		// property naming the config to use — so one env var points every
		// `dotnet restore/build/publish` at the generated feed list. A
		// repo-level NuGet.Config still wins for anyone who insists.
		Env: func(u string) []string { return []string{"RestoreConfigFile=/etc/apptown/NuGet.Config"} },
		Files: func(u string) []generatedFile {
			const tmpl = `<?xml version="1.0" encoding="utf-8"?>
<!-- GENERATED by AppTown from registry bindings - do not edit. -->
<configuration>
  <packageSources>
    <clear />
    <add key="apptown-local" value="%s" protocolVersion="3" />
  </packageSources>
</configuration>
`
			return []generatedFile{{
				Name: "NuGet.Config", ContainerPath: "/etc/apptown/NuGet.Config",
				Content: fmt.Sprintf(tmpl, strings.TrimSuffix(u, "/")+"/index.json"),
			}}
		},
		testPath: "/index.json",
	},
	{
		Format: "rubygems", Title: "RubyGems mirror", Layer: "packages",
		Mechanism: "`BUNDLE_MIRROR__HTTPS://RUBYGEMS__ORG` env; `GEMRC` → generated `/etc/apptown/gemrc`",
		// Bundler reads mirrors from env (the odd name is its documented
		// encoding of `bundle config mirror.https://rubygems.org`); bare
		// `gem install` reads the sources list from the file GEMRC names.
		Env: func(u string) []string {
			b := strings.TrimSuffix(u, "/")
			return []string{"BUNDLE_MIRROR__HTTPS://RUBYGEMS__ORG=" + b + "/", "GEMRC=/etc/apptown/gemrc"}
		},
		Files: func(u string) []generatedFile {
			return []generatedFile{{
				Name: "gemrc", ContainerPath: "/etc/apptown/gemrc",
				Content: "# GENERATED by AppTown from registry bindings - do not edit.\n:sources:\n- " + strings.TrimSuffix(u, "/") + "/\n",
			}}
		},
		testPath: "/latest_specs.4.8.gz",
	},
	{
		Format: "apache-dist", Title: "Apache dist mirror", Layer: "toolchain",
		Mechanism: "mise url_replacements in `/etc/mise/config.toml`",
		MiseConf: func(u string) string {
			b := strings.TrimSuffix(u, "/")
			return fmt.Sprintf("[settings.url_replacements]\n"+
				"\"regex:^https://archive\\\\.apache\\\\.org/dist/(.+)$\" = %q\n"+
				"\"regex:^https://(?:dlcdn|downloads)\\\\.apache\\\\.org/(.+)$\" = %q\n",
				b+"/$1", b+"/$1")
		},
		testPath: "/maven/KEYS",
	},
}

func registryAdapterFor(format string) *registryAdapter {
	for i := range registryAdapters {
		if registryAdapters[i].Format == format {
			return &registryAdapters[i]
		}
	}
	return nil
}

// registryBindings returns format → {binding name, url} for every bound
// registry. One binding per format (enforced at save).

// Ambient is the environment and generated files the ambient build
// environment contributes to a step container. Env applies to local steps
// too (inert unless exercised); the files are presented to Docker runs only.
type Ambient struct {
	Env []string
	// Files are the generated client configuration, as bytes: whoever runs
	// the container writes them in a folder of its own and mounts them
	// read-only (execution.File). Nothing is written into a project's tree,
	// so nothing there (a .apptown that is a link) can choose where they go
	// (design/operations/workers.md).
	Files []execution.File
	Bound map[string]string // format → binding name, for receipts/evidence
}

// AmbientFor generates the ambient build environment for a workspace:
// registry env vars plus the generated mise system config and cargo config,
// presented read-only. AppTown claims the SYSTEM layer of each client's
// hierarchy — /etc/mise/config.toml and an AppTown-owned CARGO_HOME in the
// cache volume — never image, user, or project files
// (design/subsystems/registries.md §4). ws only names the workspace's own
// mise installs; nothing reads or writes it.
// CLIDir is a dedicated PATH dir holding ONLY the mounted apptown CLI, put
// first in PATH so nothing (a mise shim, a project binary) can shadow `apptown`.
const CLIDir = "/opt/apptown/bin"

// miseWorkspaceKey turns a workspace path into a filesystem-safe key so each
// workspace gets its own MISE_DATA_DIR (installs + shims) — projects can't
// poison each other through a shared mise install.
func miseWorkspaceKey(ws string) string {
	b := filepath.Base(strings.TrimRight(ws, "/"))
	b = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, b)
	if b == "" || b == "." || b == ".." {
		return "default"
	}
	return b
}

// The toolchain layer (mise's system config, the workspace's own installs
// and its shims on PATH) is generated whether or not any registry is bound:
// a project's mise.toml is its declaration, and the public internet is a
// supply line too. Gating it on a binding left an AppTown with no mirrors
// never installing a declared Ruby, and agents hand-wrote Rails skeletons
// without it.
func AmbientFor(bound map[string]integration.BoundRegistry, ws string) Ambient {
	var amb Ambient
	if len(bound) > 0 {
		amb.Bound = map[string]string{}
	}

	var miseSettings, miseTables, cargoConf strings.Builder
	formats := make([]string, 0, len(bound))
	for f := range bound {
		formats = append(formats, f)
	}
	sort.Strings(formats) // deterministic generated files → stable hashes/diffs
	for _, f := range formats {
		ad := registryAdapterFor(f)
		if ad == nil {
			continue
		}
		b := bound[f]
		amb.Bound[f] = b.Name
		if ad.Env != nil {
			amb.Env = append(amb.Env, ad.Env(b.URL)...)
		}
		if ad.MiseConf != nil {
			frag := ad.MiseConf(b.URL)
			// url_replacements tables must merge into ONE [settings.url_replacements]
			if strings.HasPrefix(frag, "[settings.url_replacements]") {
				miseTables.WriteString(strings.TrimPrefix(frag, "[settings.url_replacements]\n"))
			} else {
				miseSettings.WriteString(frag)
			}
		}
		if ad.CargoConf != nil {
			cargoConf.WriteString(ad.CargoConf(b.URL))
		}
	}

	genFiles := []generatedFile{}
	for _, f := range formats {
		if ad := registryAdapterFor(f); ad != nil && ad.Files != nil {
			genFiles = append(genFiles, ad.Files(bound[f].URL)...)
		}
	}

	// Maven's own tarball: mise's aqua backend fetches it from
	// archive.apache.org, which throttles and refuses connections; a java
	// tests gate can fail "mvn: not found" on it. Without an
	// Apache dist mirror bound, the same release comes from Maven Central —
	// through the bound Maven repository when there is one (it mirrors
	// central), otherwise from Central itself.
	if _, ok := bound["apache-dist"]; !ok {
		central := "https://repo.maven.apache.org/maven2"
		if m, ok := bound["maven"]; ok {
			central = strings.TrimSuffix(m.URL, "/")
		}
		fmt.Fprintf(&miseTables, "\"regex:^https://archive\\\\.apache\\\\.org/dist/maven/maven-3/([^/]+)/binaries/(apache-maven-[^/]+)$\" = %q\n",
			central+"/org/apache/maven/apache-maven/$1/$2")
	}

	// mise system config: registry routing + ubi disabled (structurally
	// unmirrorable — fail loudly, never silently bypass). Generated even if
	// only env-driven formats are bound, so ubi stays off everywhere.
	mise := "# GENERATED by AppTown from registry bindings — do not edit.\n" +
		"# Project mise.toml wins on conflict (system layer).\n" +
		"[settings]\ndisable_backends = [\"ubi\"]\n" +
		// Precompiled interpreters only: the compile path (pyenv git clone +
		// source fetches) is unmirrorable; python-build-standalone rides the
		// github-releases rule.
		"python.compile = false\n" +
		// A refused or dropped download is tried again before the install
		// fails: a toolchain that installs on the second try is not a failed
		// gate.
		"http_retries = 3\n" + miseSettings.String()
	if miseTables.Len() > 0 {
		mise += "\n[settings.url_replacements]\n" + miseTables.String()
	}
	amb.Files = append(amb.Files, execution.File{Path: "/etc/mise/config.toml", Content: []byte(mise), Mode: 0o644})
	// mise runtime. Installs + shims are MUTABLE and go PER-WORKSPACE, never in
	// the shared volume: a shared mise install let one project's stray dep (a
	// Python package shipping a `apptown` console script) create a `apptown` shim
	// that shadowed AppTown CLI in EVERY container (complaint #46). Projects
	// must not be able to poison each other; a workspace poisoning itself is its
	// own business. Only the CONTENT-ADDRESSED download cache (MISE_CACHE_DIR,
	// keyed by version/hash — nothing a project can subvert) stays shared, so
	// isolation costs a re-extract, not a re-download.
	miseData := "/apptown-cache/mise/ws/" + miseWorkspaceKey(ws)
	amb.Env = append(amb.Env,
		"MISE_DATA_DIR="+miseData,
		"MISE_CACHE_DIR="+execution.ToolCacheMount,
		"MISE_YES=1",
		"MISE_TRUSTED_CONFIG_PATHS=/work:/etc/mise",
		// AppTown CLI's own dir comes FIRST, so a mise shim can never shadow
		// it (belt-and-suspenders on top of the per-workspace isolation above).
		// Then this workspace's shims, so declared tools resolve without `mise
		// exec` fumbling (an agent can lose minutes finding a tool not on PATH).
		"PATH="+CLIDir+":"+miseData+"/shims:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	)

	for _, gf := range genFiles {
		amb.Files = append(amb.Files, execution.File{Path: gf.ContainerPath, Content: []byte(gf.Content), Mode: 0o644})
	}

	if cargoConf.Len() > 0 {
		body := "# GENERATED by AppTown from registry bindings — do not edit.\n" +
			"# A repo's own .cargo/config.toml wins on conflict (cwd-upward hierarchy).\n" + cargoConf.String()
		// AppTown-owned CARGO_HOME inside the cache volume (also caches the
		// registry index + crates); config presented over it read-only.
		amb.Env = append(amb.Env, "CARGO_HOME=/apptown-cache/cargo")
		amb.Files = append(amb.Files, execution.File{Path: "/apptown-cache/cargo/config.toml", Content: []byte(body), Mode: 0o644})
	}
	return amb
}

// ProbeRegistry probes a bound URL with the format's known-small artifact.
func ProbeRegistry(format, url string) integration.Health {
	ad := registryAdapterFor(format)
	if ad == nil {
		return integration.Health{OK: false, Detail: "unknown format " + format}
	}
	if url == "" {
		return integration.Health{OK: false, Detail: "url is required"}
	}
	probe := strings.TrimSuffix(url, "/") + ad.testPath
	// A registry: the deployments use (internal/trust).
	client := &http.Client{Timeout: 20 * time.Second, Transport: trust.Transport(trust.Deployments)}
	defer client.CloseIdleConnections()
	// One GET, of which at most 64 KiB is read: a large artifact is not
	// downloaded, and its answer is the registry's answer, never a second
	// request's.
	resp, err := client.Get(probe)
	if err != nil {
		return integration.Health{OK: false, Detail: "probe failed: " + err.Error()}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	detail := fmt.Sprintf("GET %s → %d", ad.testPath, resp.StatusCode)
	return integration.Health{OK: resp.StatusCode >= 200 && resp.StatusCode < 300, Detail: detail}
}

// DeclaresToolchain: does the workspace opt in to mise? The repo file IS
// the opt-in (design/subsystems/registries.md §0).
func DeclaresToolchain(ws string) bool {
	for _, n := range []string{"mise.toml", ".mise.toml", ".config/mise/config.toml"} {
		if _, err := os.Stat(filepath.Join(ws, n)); err == nil {
			return true
		}
	}
	return false
}

// ResolvedToolchain parses the mise ls --current --json snapshot the
// implicit install leaves behind: tool → resolved version, for step evidence.
func ResolvedToolchain(ws string) map[string]string {
	raw, err := os.ReadFile(filepath.Join(ws, ".apptown", "mise-resolved.json"))
	if err != nil {
		return nil
	}
	var parsed map[string][]struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return nil
	}
	out := map[string]string{}
	for tool, entries := range parsed {
		if len(entries) > 0 && entries[0].Version != "" {
			out[tool] = entries[0].Version
		}
	}
	return out
}

func RegistryFormats() []string {
	out := make([]string, 0, len(registryAdapters))
	for _, a := range registryAdapters {
		out = append(out, a.Format)
	}
	return out
}
