package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"flokkit.io/forge/internal/forge/dependency"

	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/forge/secret"
)

// Configuration validates a caller's declaration and renders local development files.
type Configuration struct{ catalog *dependency.Catalog }

func NewConfiguration(catalog *dependency.Catalog) Configuration {
	return Configuration{catalog: catalog}
}

// The forge-native contract's tooling surface (design/decisions/forge-native-contract.md):
// a schema models can author against, a validator they can run, and the
// local-dev compose generator. All take the .apptown/app.yaml text so they work
// on the dev's local working copy, not only what's committed.

// handleConfigSchema: GET /api/v1/config/schema — the JSON Schema for
// .apptown/app.yaml. Models fetch this to author a contract; editors can use it
// for completion. Semantic rules the schema can't express (exactly one public
// part, needs must be in the catalog) are enforced by validate below.
func (h Configuration) ConfigSchema(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(appcontract.JSONSchema)
}

// The contract tooling's responses.
type (
	// ContractUnparsed answers a contract that does not parse at all.
	ContractUnparsed struct {
		OK     bool     `json:"ok"`
		Errors []string `json:"errors"`
	}
	// ContractCheck answers a contract that parsed: its errors, warnings and shape.
	ContractCheck struct {
		OK       bool            `json:"ok"`
		Errors   []string        `json:"errors"`
		Warnings []string        `json:"warnings"`
		Summary  ContractSummary `json:"summary"`
	}
	ContractSummary struct {
		Parts []string `json:"parts"`
		Needs []string `json:"needs"`
	}
	// DevComposition is a contract rendered for local development.
	DevComposition struct {
		Compose  string            `json:"compose"`
		Env      map[string]string `json:"env"`
		Procfile string            `json:"procfile"`
	}
)

// handleConfigValidate: POST /api/v1/config/validate {appYaml} — structural +
// semantic validation. Returns {ok, errors, warnings, summary}.
func (h Configuration) ConfigValidate(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{errorText: "appYaml required"}, func(in struct {
		AppYAML    string `json:"appYaml"`
		SecretSpec string `json:"secretSpec"` // secretspec.toml, when the repo has one
	}) {
		if strings.TrimSpace(in.AppYAML) == "" {
			WriteError(w, 400, "appYaml required")
			return
		}

		c, err := appcontract.Parse([]byte(in.AppYAML))
		if err != nil {
			WriteJSON(w, 200, ContractUnparsed{OK: false, Errors: []string{err.Error()}})
			return
		}
		var errs, warns []string
		// The secret declaration is read by the same parser boot uses, so a
		// shape boot would refuse (a secretspec.toml without [project]
		// revision, say) is refused here first.
		if strings.TrimSpace(in.SecretSpec) != "" {
			if spec, err := secret.ParseSpec([]byte(in.SecretSpec)); err != nil {
				errs = append(errs, secret.SpecFile+": "+err.Error())
			} else {
				for _, ig := range spec.Ignored {
					warns = append(warns, secret.SpecFile+": "+ig+" is not read by AppTown")
				}
			}
		}
		// Needs must resolve to catalog capabilities, each named; then the
		// catalogue's refusal of them as declared, as a release asks it.
		known := true
		for _, n := range c.Needs {
			if h.dependencyKind(r.Context(), n) == nil {
				known = false
				errs = append(errs, "need "+n+" is not in the dependency catalog (see GET /api/v1/dependencies)")
			}
		}
		if known {
			if why, err := h.catalog.RefusesNeeds(r.Context(), c.Needs, c.NeedParams); err != nil {
				errs = append(errs, "the needs could not be checked: "+err.Error())
			} else if why != "" {
				errs = append(errs, why)
			}
		}
		for _, name := range append([]string{appcontract.DefaultProfile}, c.ProfileNames()...) {
			if _, err := h.catalog.ResolveSizes(r.Context(), c.ForProfile(name)); err != nil {
				errs = append(errs, "profile "+name+": "+err.Error())
			}
		}
		// A field the forge accepts but never runs must say so, not promise
		// data.
		for _, p := range c.Parts {
			if p.Seed != "" {
				warns = append(warns, "app part "+p.Name+" declares seed:, which AppTown does not run anywhere; put a command that must run before the app starts in an init step")
			}
		}
		parts := make([]string, 0, len(c.Parts))
		for _, p := range c.Parts {
			tag := p.Name
			if p.Public {
				tag += " (public)"
			}
			parts = append(parts, tag)
		}
		WriteJSON(w, 200, ContractCheck{
			OK: len(errs) == 0, Errors: errs, Warnings: warns,
			Summary: ContractSummary{Parts: parts, Needs: c.Needs},
		})
	})
}

// handleDevCompose: POST /api/v1/dev-compose {appYaml, mode} — render the
// contract to a docker-compose.yml for local dev. mode "needs" (default)
// renders ONLY the needs (process-mode: you run the app yourself) and returns
// the localhost-resolved connection env for a .env; mode "full" renders the
// whole app (container-mode). Instance-level (uses the catalog); takes the
// app.yaml text so it renders the dev's local working copy.
func (h Configuration) DevCompose(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{errorText: "appYaml required"}, func(in struct {
		AppYAML string `json:"appYaml"`
		Mode    string `json:"mode"`
	}) {
		if strings.TrimSpace(in.AppYAML) == "" {
			WriteError(w, 400, "appYaml required")
			return
		}

		c, err := appcontract.Parse([]byte(in.AppYAML))
		if err != nil {
			WriteError(w, 400, "%v", err)
			return
		}
		var compose string
		if in.Mode == "full" {
			compose, err = h.generateDevCompose(r.Context(), c)
		} else {
			compose, err = h.generateNeedsCompose(r.Context(), c)
		}
		if err != nil {
			WriteError(w, 400, "%v", err)
			return
		}
		// Process-mode env: the app runs on the host, so the needs are reached at
		// localhost:<published-port>. Plus a Procfile when the app is multi-process.
		env := h.catalog.ResolveLocal(r.Context(), c.Needs, c.NeedParams, "localhost").Env
		WriteJSON(w, 200, DevComposition{Compose: compose, Env: env, Procfile: generateProcfile(c)})
	})
}

func (h Configuration) dependencyKind(ctx context.Context, capability string) *dependency.Kind {
	kind, err := h.catalog.ByCapability(ctx, capability)
	if err != nil {
		if !errors.Is(err, dependency.ErrUnknown) && !errors.Is(err, dependency.ErrSpec) {
			log.Printf("reading the dependency kind %s: %v", capability, err)
		}
		return nil
	}
	return kind
}
