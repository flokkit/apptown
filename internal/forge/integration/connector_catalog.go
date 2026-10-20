package integration

import (
	"strings"

	"flokkit.io/forge/internal/deploy/digitalocean"
)

// A preset is a pre-filled configuration of a generic spec — the catalog
// shows it as its own tile (Harness-style: "Semgrep", not "Scanner (tool)").
type ConnectorPreset struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Icon        string         `json:"icon"`
	Description string         `json:"description"`
	Suggest     string         `json:"suggest,omitempty"` // suggested binding name
	Config      map[string]any `json:"config"`
}

type ConnectorField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Type        string `json:"type"` // string | number (a JSON number; empty is absent) | bool | secretRef | modelList | command (space-split) | select
	Help        string `json:"help,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Options     string `json:"options,omitempty"` // for select: comma-separated
}

type ConnectorSpec struct {
	Capability  string            `json:"capability"`
	Provider    string            `json:"provider"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Multi       bool              `json:"multi"`   // may the user create additional bindings?
	Managed     bool              `json:"managed"` // configured by AppTown itself (flags/seeds)
	Icon        string            `json:"icon,omitempty"`
	Fields      []ConnectorField  `json:"fields"`
	Presets     []ConnectorPreset `json:"presets,omitempty"`
}

func ConnectorCatalog() []ConnectorSpec {
	return []ConnectorSpec{
		{
			Capability: "scanner", Provider: "tool", Title: "Scanner (tool)", Multi: true, Icon: "scanner",
			Description: "Runs a container against the code: exit 0 = clean, 1 = findings. The report file is stored with the run. Covers SCA, SAST, secrets, SBOM, license and lint tools.",
			Presets: []ConnectorPreset{
				{Name: "semgrep", Title: "Semgrep", Icon: "semgrep", Suggest: "semgrep",
					Description: "SAST — static analysis with community rules (--config auto).",
					// use-tls is excluded by construction: an app serves plain HTTP
					// inside its container and TLS ends at AppTown's front door, so
					// the rule can never be acted on here (excluding it per project
					// would be the wrong tool).
					Config: map[string]any{
						"description": "SAST via semgrep", "image": "semgrep/semgrep:1.99.0",
						"command": "semgrep scan --config auto --exclude-rule go.lang.security.audit.net.use-tls.use-tls --sarif --output /work/.apptown/semgrep.sarif /work",
						"report":  ".apptown/semgrep.sarif", "format": "sarif", "kind": "sast", "network": true,
					}},
				{Name: "trivy", Title: "Trivy", Icon: "trivy", Suggest: "trivy",
					Description: "Filesystem vulnerability and misconfiguration scanning.",
					Config: map[string]any{
						"description": "vuln/misconfig scanning via trivy", "image": "aquasec/trivy:0.66.0",
						"command": "fs --scanners vuln,misconfig --format sarif --output /work/.apptown/trivy.sarif /work",
						"report":  ".apptown/trivy.sarif", "format": "sarif", "kind": "vuln", "network": true,
					}},
				{Name: "osv", Title: "osv-scanner", Icon: "osv", Suggest: "osv-custom",
					Description: "SCA — dependency vulnerabilities via the OSV database.",
					Config: map[string]any{
						"description": "dependency scanning via osv-scanner", "image": "ghcr.io/google/osv-scanner:v2.2.4",
						"command": "scan --format json --output /work/.apptown/osv-report.json -r /work",
						"report":  ".apptown/osv-report.json", "format": "osv", "kind": "vuln", "network": true,
					}},
				{Name: "gitleaks", Title: "Gitleaks", Icon: "gitleaks", Suggest: "gitleaks-custom",
					Description: "Secrets detection — leaked keys and tokens in the tree.",
					Config: map[string]any{
						"description": "secrets scanning via gitleaks", "image": "zricethezav/gitleaks:v8.30.0",
						"command": "detect --source /work --no-git --redact --report-format sarif --report-path /work/.apptown/gitleaks-report.sarif --exit-code 1",
						"report":  ".apptown/gitleaks-report.sarif", "format": "sarif", "kind": "secret",
					}},
				{Name: "syft", Title: "Syft (SBOM)", Icon: "sbom", Suggest: "sbom-custom",
					Description: "CycloneDX SBOM of the workspace — evidence, not findings.",
					Config: map[string]any{
						"description": "SBOM generation via syft", "image": "anchore/syft:v1.29.1",
						"command": "scan dir:/work -o cyclonedx-json=/work/.apptown/sbom.cdx.json -q",
						"report":  ".apptown/sbom.cdx.json", "artifactKind": "sbom",
					}},
				{Name: "custom", Title: "Custom scanner", Icon: "scanner",
					Description: "Any image with the exit-code contract: 0 clean, 1 findings.",
					Config:      map[string]any{}},
			},
			Fields: []ConnectorField{
				{Key: "description", Label: "Description", Type: "string"},
				{Key: "image", Label: "Image", Type: "string", Placeholder: "semgrep/semgrep:1.99.0", Help: "Pin a tag — the config hash is pinned into run receipts."},
				{Key: "command", Label: "Command", Type: "command", Placeholder: "semgrep scan --sarif --output /work/.apptown/semgrep.sarif /work", Help: "Args to the image entrypoint. Exit 0 = clean, 1 = findings, else error."},
				{Key: "report", Label: "Report path", Type: "string", Placeholder: ".apptown/semgrep.sarif", Help: "Workspace-relative report file, attached as evidence."},
				{Key: "format", Label: "Findings format", Type: "select", Options: ",sarif,osv", Help: "Enables the structured findings layer; empty = exit-code verdict only."},
				{Key: "kind", Label: "Finding kind", Type: "select", Options: ",vuln,secret,sast,license", Help: "Labels SARIF findings."},
				{Key: "network", Label: "Network egress", Type: "bool", Help: "Off = --network none. Only enable for tools that query databases."},
				{Key: "artifactKind", Label: "Artifact kind", Type: "string", Placeholder: "scan-report", Help: "Override for non-finding outputs, e.g. sbom."},
			},
		},
		{
			Capability: "model", Provider: "pi", Title: "Model provider (pi)", Multi: true, Icon: "model",
			Description: "An inference backend reached through pi. Credentials stay on AppTown's server; agents authenticate to its private inference proxy with their session token.",
			Presets: []ConnectorPreset{
				{Name: "openrouter", Title: "OpenRouter", Icon: "openrouter", Suggest: "openrouter",
					Description: "Paid multi-model gateway; pi's built-in provider. Curate which models are enabled.",
					Config:      map[string]any{"model": "openrouter/~anthropic/claude-haiku-latest", "secretEnv": []string{"OPENROUTER_API_KEY"}, "models": []string{}}},
				{Name: "anthropic", Title: "Anthropic", Icon: "claude", Suggest: "anthropic",
					Description: "Direct Anthropic API; pi's built-in provider.",
					Config:      map[string]any{"model": "anthropic/claude-haiku-latest", "secretEnv": []string{"ANTHROPIC_API_KEY"}}},
				{Name: "endpoint", Title: "Custom endpoint", Icon: "gpu",
					Description: "Self-hosted OpenAI-compatible server: LM Studio, vLLM, llama.cpp, a GPU box.",
					Config: map[string]any{"api": "openai-completions",
						"modelsJson": `[{"id":"my-model","contextWindow":131072,"maxTokens":16384}]`}},
			},
			Fields: []ConnectorField{
				{Key: "model", Label: "Default model pattern", Type: "string", Placeholder: "openrouter/~anthropic/claude-haiku-latest"},
				{Key: "concurrency", Label: "Steps at once", Type: "number", Placeholder: "8", Help: "How many steps this backend serves at the same time: a GPU box its stream count, a hosted API what its plan allows. Empty = 8 for a hosted provider, 1 for a custom endpoint. Steps beyond it wait their turn here, not at the server."},
				{Key: "secretEnv", Label: "Provider credentials", Type: "secretRef", Help: "Secret references used by AppTown inference proxy for built-in providers; never injected into agent containers."},
				{Key: "models", Label: "Enabled models", Type: "modelList", Help: "Curated list offered to model-slot selectors as <binding>/<id>. Discovery pulls the provider's live catalog."},
				{Key: "baseUrl", Label: "Base URL", Type: "string", Placeholder: "https://my-inference-host/v1", Help: "For CUSTOM OpenAI-compatible endpoints (LM Studio, vLLM…). Leave empty for built-in providers."},
				{Key: "api", Label: "API dialect", Type: "select", Options: ",openai-completions,openai-responses,anthropic-messages", Help: "Custom endpoints only."},
				{Key: "apiKeyFrom", Label: "Endpoint API key", Type: "secretRef", Help: "Secret holding the custom endpoint's key; resolved only on AppTown's server."},
				{Key: "modelsJson", Label: "Models (JSON array)", Type: "string", Placeholder: "[{\"id\":\"my-model\",\"contextWindow\":131072,\"maxTokens\":16384}]", Help: "pi model entries for the custom endpoint."},
			},
		},
		{
			Capability: CapRegistry, Provider: "registry", Title: "Package registry / mirror", Multi: true, Icon: "registry",
			Description: "A local package or toolchain mirror. One binding per format (npm, go-proxy, …); build containers are pointed at the bound mirrors automatically.",
			Fields: []ConnectorField{
				{Key: "format", Label: "Format", Type: "select",
					Options: "go-proxy,npm,pypi,cargo,maven,nuget,rubygems,rust-dist,github-releases,gitlab-releases,node-dist,golang-dist,apache-dist",
					Help:    "The contract consumers program against — drives env vars and generated mise/cargo config."},
				{Key: "url", Label: "URL", Type: "string", Placeholder: "https://nexus.example.com/repository/npm-proxy/",
					Help: "The local endpoint. Repos never contain this URL — AppTown injects it at step start."},
			},
			Presets: []ConnectorPreset{
				{Name: "go-proxy", Title: "Go modules", Icon: "go", Suggest: "go-modules",
					Description: "GOPROXY for every build step.",
					Config:      map[string]any{"format": "go-proxy", "path": "repository/go-proxy"}},
				{Name: "npm", Title: "npm", Icon: "npm", Suggest: "npm",
					Description: "Default npm registry for every build step.",
					Config:      map[string]any{"format": "npm", "path": "repository/npm-proxy/"}},
				{Name: "pypi", Title: "PyPI", Icon: "pypi", Suggest: "pypi",
					Description: "uv/pip index + mise pipx routing.",
					Config:      map[string]any{"format": "pypi", "path": "repository/pypi-proxy/simple"}},
				{Name: "cargo", Title: "Cargo (crates.io)", Icon: "rust", Suggest: "cargo",
					Description: "Sparse-index source replacement via generated cargo config.",
					Config:      map[string]any{"format": "cargo", "path": "repository/cargo-proxy/"}},
				{Name: "github-releases", Title: "GitHub releases", Icon: "github", Suggest: "github-mirror",
					Description: "mise url_replacements: toolchains + aqua/github backends.",
					Config:      map[string]any{"format": "github-releases", "path": "repository/github"}},
				{Name: "gitlab-releases", Title: "GitLab releases", Icon: "gitlab", Suggest: "gitlab-mirror",
					Description: "Mirror for gitlab.com release assets (glab, gitlab-hosted tools).",
					Config:      map[string]any{"format": "gitlab-releases", "path": "repository/gitlab-releases"}},
				{Name: "node-dist", Title: "Node.js dist", Icon: "node", Suggest: "node-dist",
					Description: "Node tarballs + SHASUMS for mise installs.",
					Config:      map[string]any{"format": "node-dist", "path": "repository/nodejs-dist/dist/"}},
				{Name: "golang-dist", Title: "Go toolchain dist", Icon: "go", Suggest: "golang-dist",
					Description: "Go toolchain tarballs for mise installs.",
					Config:      map[string]any{"format": "golang-dist", "path": "repository/golang-dist/go"}},
				{Name: "rust-dist", Title: "Rust toolchain", Icon: "rust", Suggest: "rust-dist",
					Description: "rustup dist server mirror (RUSTUP_DIST_SERVER) for toolchain installs.",
					Config:      map[string]any{"format": "rust-dist", "path": "repository/rust-dist"}},
				{Name: "maven", Title: "Maven (Central)", Icon: "maven", Suggest: "maven",
					Description: "Mirror for Maven dependency traffic via generated settings.xml.",
					Config:      map[string]any{"format": "maven", "path": "repository/maven-public/"}},
				{Name: "nuget", Title: "NuGet", Icon: "dotnet", Suggest: "nuget",
					Description: "Feed for every dotnet restore via the generated NuGet.Config (RestoreConfigFile).",
					Config:      map[string]any{"format": "nuget", "path": "repository/nuget-proxy/"}},
				{Name: "rubygems", Title: "RubyGems", Icon: "ruby", Suggest: "rubygems",
					Description: "Bundler mirror + gem sources for every build step.",
					Config:      map[string]any{"format": "rubygems", "path": "repository/rubygems-proxy/"}},
				{Name: "apache-dist", Title: "Apache dist", Icon: "maven", Suggest: "apache-dist",
					Description: "Apache releases (maven, …) for mise installs.",
					Config:      map[string]any{"format": "apache-dist", "path": "repository/apache-dist/dist"}},
			},
		},
		{
			Capability: "feedback", Provider: "customer-feedback", Title: "Customer feedback", Multi: true, Icon: "feedback",
			Description: "Experimental: one project uses it, and its service registers without signing in. A feedback button for apps. Connect the Customer Feedback service once; attaching a project registers it there and gives its agents and app containers the widget snippet and app key.",
			Fields: []ConnectorField{
				{Key: "url", Label: "Service URL", Type: "string", Placeholder: "https://feedback.example.com",
					Help: "Where the Customer Feedback service runs. Registration is unauthenticated by design (intranet service)."},
			},
		},
		{
			Capability: "errors", Provider: "glitchtip", Title: "Error reporting (GlitchTip)", Multi: true, Icon: "errors",
			Description: "Sentry-compatible error reporting. Bind an admin token once; attaching a project creates a GlitchTip project and injects its DSN into the project's app containers.",
			Fields: []ConnectorField{
				{Key: "url", Label: "GlitchTip URL", Type: "string", Placeholder: "https://errors.example.com"},
				{Key: "org", Label: "Organization slug", Type: "string", Placeholder: "myorg", Help: "Provider-side organization the projects are created in."},
				{Key: "team", Label: "Team slug (optional)", Type: "string", Placeholder: "", Help: "Projects live under a team. Empty = use the org's first team, creating a 'forge' team if none exist."},
				{Key: "tokenFrom", Label: "Admin API token", Type: "secretRef", Help: "GlitchTip auth token with project:read project:write team:read team:write org:read — resolved via the secrets capability, never stored."},
			},
			Presets: []ConnectorPreset{
				{Name: "glitchtip", Title: "GlitchTip", Icon: "errors", Suggest: "glitchtip",
					Description: "Self-hosted Sentry-compatible error tracking.",
					Config:      map[string]any{"url": "", "org": "", "tokenFrom": []string{"GLITCHTIP_TOKEN"}, "instanceURL": "errors"}},
			},
		},
		{
			Capability: CapLLM, Provider: "openrouter", Title: "AI models (OpenRouter)", Multi: true, Icon: "model",
			Description: "AI models for the apps people build here. Bind an OpenRouter provisioning key once; a project that attaches AI models gets its own OpenRouter key, with a spending limit, for each deployment and each preview of an app that declares the llm need. The keys are revoked when the deployment is taken down or the preview ends. The provisioning key stays on AppTown's server.",
			Fields: []ConnectorField{
				{Key: "provisioningKeyFrom", Label: "Provisioning key", Type: "secretRef", Placeholder: "OPENROUTER_PROVISIONING_KEY",
					Help: "The secret holding an OpenRouter provisioning key (Settings › Provisioning API keys at openrouter.ai). Read only on AppTown's server; a STEP_ secret is refused, because those reach workflow steps."},
				{Key: "maxLimit", Label: "Most per key ($)", Type: "number", Placeholder: "20", Help: "The largest limit any one key may have, in US dollars. An app that asks for more is refused."},
				{Key: "defaultLimit", Label: "Default limit ($)", Type: "number", Placeholder: "1", Help: "A deployment's key limit when the app declares none. Empty = $1."},
				{Key: "previewLimit", Label: "Preview limit ($)", Type: "number", Placeholder: "1", Help: "Every preview's key limit, whatever the app declares. Empty = $1."},
				{Key: "url", Label: "API URL", Type: "string", Placeholder: "https://openrouter.ai/api/v1", Help: "Empty = OpenRouter. Apps get it as OPENROUTER_BASE_URL."},
			},
		},
		{
			Capability: CapSCM, Provider: "gitlab", Title: "GitLab", Managed: true, Icon: "gitlab",
			Description: "Repos, branches, merge requests. Configured via Infisical (GITLAB_URL, GITLAB_TOKEN) and the GIT_ASKPASS helper.",
		},
		{
			Capability: "secrets", Provider: "infisical", Title: "Infisical", Managed: true, Icon: "infisical",
			Description: "Secrets kept in Infisical and read by AppTown's server. Keys starting STEP_ are given to step containers and model credentials to agent containers. A project's application secrets stored in Infisical (Settings → Secrets) are read from the same Infisical project and environment.",
		},
		{
			Capability: "secrets", Provider: "internal", Title: "Built-in secret store", Managed: true, Icon: "secrets",
			Description: "Keeps application secrets in AppTown's own database, obfuscated with a key AppTown generated. It is built into AppTown's server, with no separate service. Obfuscation prevents accidental disclosure, but this is not a vault: anyone with the database can read the values. Use Infisical for anything that matters.",
		},
		{
			Capability: "executor", Provider: "docker", Title: "Docker executor", Managed: true, Icon: "docker",
			Description: "Runs workflow steps in Docker containers. The image and cache volume are set by apptownd's command-line flags.",
		},
		{
			Capability: "deploy", Provider: "docker", Title: "Docker deploy", Managed: true, Icon: "deploy",
			Description: "Runs applications as containers, as described by their .apptown/compose.yaml.",
		},
		{
			Capability: digitalocean.CapCloudAccount, Provider: "digitalocean", Title: "DigitalOcean", Multi: true, Icon: "deploy",
			Description: "A DigitalOcean team: App Platform runs the app, Managed Databases hold its data, the container registry holds its images. Each connection offers one App Platform deployment target. The token is given as a secret name and read when it is used.",
			Fields: []ConnectorField{
				{Key: "tokenFrom", Label: "API token secret", Type: "secretRef", Placeholder: "DIGITALOCEAN_ACCESS_TOKEN", Help: "The name of the secret holding a DigitalOcean API token (apps, databases, registry)."},
				{Key: "region", Label: "Region", Type: "select", Options: "syd,sgp,blr,nyc,sfo,tor,lon,fra,ams", Help: "The App Platform region; the database goes to its sibling region."},
				{Key: "registry", Label: "Container registry", Type: "string", Placeholder: "forge", Help: "The DOCR registry name — created on first use if the account has none."},
				{Key: "instanceSize", Label: "Instance size", Type: "string", Placeholder: "basic-xxs", Help: "App Platform instance size slug for every part."},
				{Key: "dbSize", Label: "Database size", Type: "string", Placeholder: "db-s-1vcpu-1gb", Help: "Managed database node size."},
			},
		},
	}

}

// CatalogDefaults: the facts that vary per installation and used to be
// compiled into the catalog — one installation's mirror and error-tracker
// hostnames showed on the first screen of every other instance. Set from apptownd's flags; empty means "no suggestion".
type CatalogDefaults struct {
	MirrorBase string // e.g. https://nexus.example.com — presets carry only the repository path
	ErrorsURL  string // the org's error tracker, e.g. https://errors.example.com
}

// InstancePresets fills a connector's preset URLs from the instance
// defaults: a registry preset's "path" becomes MirrorBase/path; a preset
// marked instanceURL: errors gets ErrorsURL. With nothing configured the
// URL stays empty and the form's placeholder does the suggesting.
func InstancePresets(c ConnectorSpec, defaults CatalogDefaults) ConnectorSpec {
	if len(c.Presets) == 0 {
		return c
	}
	cc := c
	cc.Presets = make([]ConnectorPreset, len(c.Presets))
	for i, p := range c.Presets {
		cfg := make(map[string]any, len(p.Config))
		for k, v := range p.Config {
			cfg[k] = v
		}
		if path, ok := cfg["path"].(string); ok {
			if defaults.MirrorBase != "" {
				cfg["url"] = strings.TrimSuffix(defaults.MirrorBase, "/") + "/" + path
			} else {
				cfg["url"] = ""
			}
		}
		if cfg["instanceURL"] == "errors" {
			cfg["url"] = defaults.ErrorsURL
			delete(cfg, "instanceURL")
		}
		p.Config = cfg
		cc.Presets[i] = p
	}
	return cc
}
func FindConnector(capability, provider string) *ConnectorSpec {
	catalog := ConnectorCatalog()
	for i := range catalog {
		if catalog[i].Capability == capability && catalog[i].Provider == provider {
			return &catalog[i]
		}
	}
	return nil
}

// ServiceConnectorSpec derives a connector-catalog spec from a kind so the
// existing Integrations UI can create admin connectors for it.
func ServiceConnectorSpec(k *Definition) ConnectorSpec {
	fields := make([]ConnectorField, 0, len(k.Connector.Fields))
	for _, f := range k.Connector.Fields {
		t := f.Type
		if t == "" {
			t = "string"
		}
		fields = append(fields, ConnectorField{Key: f.Key, Label: f.Label, Type: t, Help: f.Help, Placeholder: f.Placeholder})
	}
	return ConnectorSpec{
		Capability: k.Capability, Provider: "service", Title: k.Title, Multi: true,
		Icon: k.Icon, Description: k.Description + " (runtime-defined service kind)",
		Fields: fields,
	}
}
