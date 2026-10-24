package httpapi

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/inference"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/openrouter"
)

type SetupModel struct {
	secrets      *secret.Service
	integrations *integration.Service
	models       *model.Slots
	db           *database.DB
	now          func() time.Time // AppTown's clock, which stamps its receipts; nil is the real one
	// openRouterBase is OpenRouter's API root, whose public model list
	// resolves its latest-Sonnet alias; tests point it at a fake.
	openRouterBase string
	// anthropicBase is Anthropic's API root, whose model list (asked with
	// the key being set up) names its newest Sonnet; tests point it at a fake.
	anthropicBase string
}

func NewSetupModel(secrets *secret.Service, integrations *integration.Service, models *model.Slots, db *database.DB, now func() time.Time) SetupModel {
	return SetupModel{secrets: secrets, integrations: integrations, models: models, db: db, now: now,
		openRouterBase: openrouter.DefaultBase, anthropicBase: inference.AnthropicBase}
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

// A first Anthropic key's default model is the newest Sonnet in Anthropic's
// own model list, asked with that key. Anthropic has no "latest" alias: the
// earlier default, anthropic/claude-sonnet-latest, named no model and every
// chat failed. anthropicFallback is used only when the list cannot be read,
// and is replaced at the next setup.
const anthropicFallback = "anthropic/claude-sonnet-5-5"

// anthropicSonnet is "anthropic/" and the newest Sonnet Anthropic lists for
// key, or anthropicFallback when its list cannot say.
func (h SetupModel) anthropicSonnet(ctx context.Context, key string) string {
	id, err := inference.AnthropicNewest(ctx, h.anthropicBase, key, "claude-sonnet-")
	if err != nil {
		return anthropicFallback
	}
	return "anthropic/" + id
}

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
		var secretName string
		switch in.Provider {
		case "anthropic":
			secretName = "ANTHROPIC_API_KEY"
		case "openrouter":
			secretName = "OPENROUTER_API_KEY"
		default:
			WriteError(w, 400, "provider must be anthropic or openrouter")
			return
		}
		model := strings.TrimSpace(in.Model)
		if model == "" {
			switch in.Provider {
			case "anthropic":
				model = h.anthropicSonnet(r.Context(), in.Key)
			case "openrouter":
				model = h.openRouterSonnet(r.Context())
			}
		}
		// secrets.env: append or replace the key's line. 0600 — it holds values.
		if err := h.secrets.SetBootstrap(r.Context(), secretName, in.Key, Actor(r)); err != nil {
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
