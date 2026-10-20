package integration

import (
	"context"
	"encoding/json"
	"log"
	"strings"
)

// Integrations I1: name the seam. A binding is an instance-level named
// instance of a capability: provider + config. The integrations forge
// already has are seed bindings, pinned into every run's ledger and listed
// read-only with live health.
//
// Config rows hold NO secret material — only references (key names). Config
// history is append-only (integration_versions); a run pins {version,
// configHash}, so later edits never retro-change what a receipt attests.

// The compiled-in capabilities.
const (
	CapSCM      = "scm"
	CapSecrets  = "secrets"
	CapExecutor = "executor"
	CapModel    = "model"
	CapDeploy   = "deploy"
	CapRegistry = "registry"
	// Scanner bindings are supplied by Verification.
	// Coming with later phases: notifier, intake, artifacts (publish).
)

// SeedConfig is the forge runtime configuration the seed bindings record.
type SeedConfig struct {
	AgentImage       string
	AgentModel       string // provider/model; its provider names the model binding
	AgentConcurrency int
	// PortainerEnvironment is the Portainer environment the intranet
	// releases to (apptownd -portainer-environment).
	PortainerEnvironment string
}

// DefaultPortainerEnvironment is the environment name Portainer gives the
// Docker it runs on when installed with its defaults.
const DefaultPortainerEnvironment = "local"

func seedJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// Seed is one binding the forge's own configuration defines.
type Seed struct {
	Name, Capability, Provider string
	Config                     string // JSON: references only, never secret material
}

// Seeds are the integrations the forge already has, as managed bindings:
// the boot syncs them as a versioned stock kind, so a changed seed appends a
// version and an audit entry, and a binding authored under a seed's name
// (unmanaged) is kept. The model binding is not among them (SeedModelBinding).
func Seeds(cfg SeedConfig) []Seed {
	mk := func(name, capability, provider string, config map[string]any) Seed {
		return Seed{Name: name, Capability: capability, Provider: provider, Config: seedJSON(config)}
	}
	return []Seed{
		mk("gitlab", CapSCM, "gitlab", map[string]any{
			"urlFrom": "GITLAB_URL", "tokenFrom": "GITLAB_TOKEN", "auth": "PRIVATE-TOKEN + GIT_ASKPASS",
		}),
		mk("infisical", CapSecrets, "infisical", map[string]any{
			"bootstrap": "secrets.env", "auth": "universal-auth", "stepPrefix": "STEP_",
		}),
		// The built-in secret store:
		// application secrets in the forge's own database, obfuscated with
		// a generated key. Works with nothing set up; honest about being
		// protection against accidental disclosure, not a vault.
		mk("built-in-secrets", CapSecrets, "internal", map[string]any{
			"store": "secret_values", "where": "in-process: apptownd itself, no separate service",
			"protection": "obfuscated with a generated key kept in the same database",
			"limit":      "not a vault: anyone with the database can read the values; use Infisical for anything that matters",
		}),
		mk("docker", CapExecutor, "docker", map[string]any{
			"image": cfg.AgentImage, "cacheVolume": "forge-cache",
		}),
		mk("docker-deploy", CapDeploy, "docker", map[string]any{
			"target": "docker", "appcontract.Contract": ".forge/app.yaml",
		}),
		// The intranet (design/subsystems/lan-release.md): the second deploy target.
		// Same shape as every seam — provider named, secrets referenced by
		// NAME (resolved through the secrets capability at use time),
		// environment and stack conventions stated where an operator can
		// see them.
		mk("portainer-deploy", CapDeploy, "portainer", map[string]any{
			"urlFrom": "PORTAINER_URL", "apiKeyFrom": "PORTAINER_API_KEY",
			"appsDomainFrom": "FORGE_APPS_DOMAIN", "environment": cfg.PortainerEnvironment,
			// releaseDeployment: the tier name release rows carry and the
			// DEFAULT target of the big Release button (the button's face is
			// the default; other targets live behind the chevron).
			"releaseDeployment": "lan",
			"stackPrefix":       "forge-", "release": "image from committed main; digest is the receipt",
		}),
	}
}

// SeedModelBinding records the model binding named by the agent model's
// provider, once. Model bindings are UNMANAGED: the row is the source of
// truth for the provider definition (endpoint, models, secret refs). The flag
// only seeds the initial row — overwriting on boot clobbers user edits (and
// did: each restart wiped providerDef, re-triggering the host import).
func (s *Service) SeedModelBinding(ctx context.Context, cfg SeedConfig) {
	modelName := "pi-default"
	if cfg.AgentModel != "" {
		if i := strings.IndexByte(cfg.AgentModel, '/'); i > 0 {
			modelName = cfg.AgentModel[:i]
		}
	}
	haveModel, err := s.Bindings.Count(ctx, modelName, CapModel)
	if err != nil {
		log.Printf("read failed: %v", err)
	}
	if haveModel == 0 {
		// Unmanaged from the first insert: the provider definition is binding
		// config, not flag-derived.
		if _, err := s.Bindings.Upsert(ctx, modelName, CapModel, "pi", seedJSON(map[string]any{
			"model": cfg.AgentModel, "concurrency": cfg.AgentConcurrency,
		}), false); err != nil {
			log.Printf("integration %s: recording its configuration: %v", modelName, err)
		}
	}
	// NOTE: paid model providers (e.g. openrouter) are NOT seeded — they are
	// user bindings created from the connector catalog, editable and
	// deletable. "Managed" means strictly config-derived from forge runtime
	// config; a convenience seed here would resurrect after deletion and
	// wrongly wear the managed lock.
}
