package httpapi

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/model"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/openrouter"
)

type SetupModel struct {
	secrets      *secret.Service
	integrations *integration.Service
	models       *model.Slots
	db           *database.DB
	now          func() time.Time // the forge's clock, which stamps its receipts; nil is the real one
	// openRouterBase is OpenRouter's API root, whose public model list
	// resolves its latest-Sonnet alias; tests point it at a fake.
	openRouterBase string
}

func NewSetupModel(secrets *secret.Service, integrations *integration.Service, models *model.Slots, db *database.DB, now func() time.Time) SetupModel {
	return SetupModel{secrets: secrets, integrations: integrations, models: models, db: db, now: now,
		openRouterBase: openrouter.DefaultBase}
}

// A first OpenRouter key's default model is the one OpenRouter's
// latest-Sonnet alias names when the key is set up: its public model list
// gives the alias's target. The alias itself cannot be the default, because
// the inference gateway admits no "~" selector. openRouterFallback is used
// only when the list cannot be read, and is replaced at the next setup; a
// fixed version as the only default (claude-sonnet-4.5) went stale.
const (
	openRouterSonnetAlias = "~anthropic/claude-sonnet-latest"
	openRouterFallback    = "openrouter/anthropic/claude-sonnet-5.5"
)

// openRouterSonnet is "openrouter/" and the latest-Sonnet alias's target,
// or openRouterFallback when OpenRouter's list cannot say.
func (h SetupModel) openRouterSonnet(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	target, err := openrouter.AliasTarget(ctx, h.openRouterBase, openRouterSonnetAlias)
	if err != nil {
		return openRouterFallback
	}
	return "openrouter/" + target
}

// SetupModel: POST /api/v1/setup/model {provider, key, model?} — the
// one-field onboarding. Writes the key into secrets.env (the bootstrap
// store; the value is never echoed back), creates the provider binding from
// the same shape as its catalog preset, and points the default model slot
// at it.
func (h SetupModel) SetupModel(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{errorText: "bad request"}, func(in struct {
		Provider string `json:"provider"`
		Key      string `json:"key"`
		Model    string `json:"model"`
	}) {
		in.Key = strings.TrimSpace(in.Key)
		if in.Key == "" || strings.ContainsAny(in.Key, "\n\r") {
			WriteError(w, 400, "paste an API key")
			return
		}
		var secretName, defModel string
		switch in.Provider {
		case "anthropic":
			secretName, defModel = "ANTHROPIC_API_KEY", "anthropic/claude-sonnet-latest"
		case "openrouter":
			secretName = "OPENROUTER_API_KEY"
		default:
			WriteError(w, 400, "provider must be anthropic or openrouter")
			return
		}
		model := strings.TrimSpace(in.Model)
		if model == "" && in.Provider == "openrouter" {
			model = h.openRouterSonnet(r.Context())
		}
		if model == "" {
			model = defModel
		}
		// secrets.env: append or replace the key's line. 0600 — it holds values.
		if err := h.secrets.SetBootstrap(secretName, in.Key); err != nil {
			WriteError(w, 500, "writing secrets.env: %v", err)
			return
		}
		if _, err := h.integrations.Bindings.Upsert(r.Context(), in.Provider, "model", "pi", encodedJSON(map[string]any{"model": model, "secretEnv": []string{secretName}}), false); err != nil {
			log.Printf("integration %s: recording its configuration: %v", in.Provider, err)
		}
		if err := h.models.SetDefault(r.Context(), model); err != nil {
			WriteError(w, 500, "saving the default model: %v", err)
			return
		}
		audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, Actor(r), ids.Nil, "setup.model", in.Provider, map[string]any{"model": model, "secret": secretName})
		WriteJSON(w, 200, ModelConfigured{OK: true, Model: model})
	})
}

// ModelConfigured names the model first-run setup configured.
type ModelConfigured struct {
	OK    bool   `json:"ok"`
	Model string `json:"model"`
}
