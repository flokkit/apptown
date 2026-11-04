package integration

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Enabled models is an admission allowlist, including for the default.
func TestDefaultModelMustBeEnabled(t *testing.T) {
	for _, tt := range []struct {
		config  string
		refused bool
	}{
		{`{"model":"openai/stale","models":["current"]}`, true},
		{`{"model":"openai/~current","models":["current"]}`, true},
		{`{"model":"openai/~current","models":["~current"]}`, true},
		{`{"model":"openai/current","models":["current"]}`, false},
		{`{"model":"openai/~current","models":[]}`, true},
		{`{"model":"openai/current","models":[]}`, true},
		{`{"models":["current"]}`, false},
		{`{"model":"openrouter/anthropic/current","models":["anthropic/current"]}`, false},
	} {
		name := "openai"
		if strings.Contains(tt.config, "openrouter/") {
			name = "openrouter"
		}
		err := validateConfig(name, CapModel, tt.config)
		if tt.refused {
			if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "choose a default model from Enabled models") {
				t.Fatalf("%s: %v", tt.config, err)
			}
		} else if err != nil {
			t.Fatalf("%s: %v", tt.config, err)
		}
	}
}

func TestBuiltInDefaultRequiresEnabledModels(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"openai", "anthropic", "openrouter"} {
		for _, config := range []string{`{"model":"%s/current"}`, `{"model":"%s/current","models":[]}`} {
			if err := validateConfig(name, CapModel, fmt.Sprintf(config, name)); !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("%s accepted empty enabled list: %v", name, err)
			}
		}
		if err := validateConfig(name, CapModel, fmt.Sprintf(`{"model":"%s/current","models":["current"]}`, name)); err != nil {
			t.Errorf("%s refused enabled default: %v", name, err)
		}
		for _, endpoint := range []string{`"baseUrl":"http://localhost:1234"`, `"providerDef":{"baseUrl":"http://localhost:1234"}`} {
			if err := validateConfig(name, CapModel, fmt.Sprintf(`{"model":"%s/current",%s}`, name, endpoint)); err != nil {
				t.Errorf("custom endpoint default: %v", err)
			}
		}
	}
}
