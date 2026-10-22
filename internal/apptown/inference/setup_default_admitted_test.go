package inference

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
)

// The model a first OpenRouter setup stores (a concrete id, or the fallback)
// is admitted by its own binding and gets past the internal calls' model
// syntax, so the interview and chats can use it. The "~" alias could not.
//
// Kills: a "~" selector as the setup default.
func TestTheSetupDefaultModelIsAdmitted(t *testing.T) {
	const pattern = "openrouter/anthropic/claude-sonnet-5.5"
	p, err := resolveInferenceProvider("openrouter", integration.ModelConfig{
		Model: pattern, SecretEnv: []string{"OPENROUTER_API_KEY"},
	}, map[string]string{"OPENROUTER_API_KEY": "review-key"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.allows("anthropic/claude-sonnet-5.5") {
		t.Error("the setup default is not admitted by its own binding")
	}
	// The fresh setup's fast slot falls back to this default. Ask must get
	// past model syntax validation before it can reach provider resolution.
	reachedProvider := errors.New("provider resolution reached")
	g := &Gateway{providers: func(context.Context) (map[string]*inferenceProvider, error) {
		return nil, reachedProvider
	}}
	_, _, err = g.Ask(t.Context(), modelcall.Call{
		Purpose: "pack-assistance", Model: pattern,
	})
	if !errors.Is(err, reachedProvider) {
		t.Errorf("the interview cannot use the setup default: %v", err)
	}
}
