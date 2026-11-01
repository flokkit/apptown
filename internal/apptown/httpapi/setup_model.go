package httpapi

import (
	"context"
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
// the inference gateway admits no "~" selector. A first Anthropic key's
// default model is the newest Sonnet in Anthropic's own model list, asked
// with that key (Anthropic has no "latest" alias). The provider's list is
// the one source: when it cannot be read, setup fails and names why, rather
// than choosing a version AppTown keeps itself (a fixed default went stale
// and broke every chat).
const openRouterSonnetAlias = "~anthropic/claude-sonnet-latest"

// anthropicSonnet is "anthropic/" and the newest Sonnet Anthropic lists for key.
func (h SetupModel) anthropicSonnet(ctx context.Context, key string) (string, error) {
	id, err := inference.AnthropicNewest(ctx, h.anthropicBase, key, "claude-sonnet-")
	if err != nil {
		return "", err
	}
	return "anthropic/" + id, nil
}

// openRouterSonnet is "openrouter/" and the latest-Sonnet alias's target.
func (h SetupModel) openRouterSonnet(ctx context.Context) (string, error) {
	target, err := inference.OpenRouterAlias(ctx, h.openRouterBase, openRouterSonnetAlias)
	if err != nil {
		return "", err
	}
	return "openrouter/" + target, nil
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
			var err error
			switch in.Provider {
			case "anthropic":
				model, err = h.anthropicSonnet(r.Context(), in.Key)
			case "openrouter":
				model, err = h.openRouterSonnet(r.Context())
			}
			if err != nil {
				WriteError(w, 502, "AppTown could not read %s's model list to choose a model (%v); nothing was saved", in.Provider, err)
				return
			}
		}
		// secrets.env: append or replace the key's line. 0600 — it holds values.
		if err := h.secrets.SetBootstrap(r.Context(), secretName, in.Key, actor(r)); err != nil {
			WriteError(w, 500, "writing secrets.env: %v", err)
			return
		}
		// The gateway reaches the provider through its binding: without it the
		// model is not set up, whatever the default slot says.
		if _, err := h.integrations.Bindings.Upsert(r.Context(), in.Provider, "model", "pi", encodedJSON(map[string]any{"model": model, "secretEnv": []string{secretName}}), false); err != nil {
			WriteError(w, 500, "the key was saved in secrets.env, but the %s binding could not be recorded (%v); the default model was not changed", in.Provider, err)
			return
		}
		if err := h.models.SetDefault(r.Context(), model); err != nil {
			WriteError(w, 500, "saving the default model: %v", err)
			return
		}
		audit.At(clock.Or(h.now)()).BestEffort(r.Context(), h.db, actor(r), ids.Nil, "setup.model", in.Provider, map[string]any{"model": model, "secret": secretName})
		WriteJSON(w, 200, ModelConfigured{OK: true, Model: model})
	})
}

// ModelConfigured names the model first-run setup configured.
type ModelConfigured struct {
	OK    bool   `json:"ok"`
	Model string `json:"model"`
}
